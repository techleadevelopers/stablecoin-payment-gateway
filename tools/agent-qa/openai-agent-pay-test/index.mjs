#!/usr/bin/env node

import { writeFile } from 'node:fs/promises';

const DEFAULT_CARD_URL = 'https://api-production-bc748.up.railway.app/.well-known/agent-card.json';
const DEFAULT_AGENT_WALLET = '0x830000000000000000000000000000000000019a';

function parseArgs(argv) {
  const out = {};
  for (let i = 0; i < argv.length; i += 1) {
    const current = argv[i];
    if (!current.startsWith('--')) continue;
    const key = current.slice(2);
    const next = argv[i + 1];
    if (!next || next.startsWith('--')) {
      out[key] = true;
      continue;
    }
    out[key] = next;
    i += 1;
  }
  return out;
}

const args = parseArgs(process.argv.slice(2));
const cardUrl = args.card || process.env.CHAINFX_AGENT_CARD_URL || DEFAULT_CARD_URL;
const reportPath = args.out || process.env.CHAINFX_AGENT_QA_OUT || '';
const bearer = args.bearer || process.env.CHAINFX_API_KEY || process.env.OPENAI_AGENT_QA_BEARER || '';
const amountBRL = String(args.amount || process.env.CHAINFX_AMOUNT_BRL || '10.00');
const pixKey = args.pix || process.env.CHAINFX_PIX_KEY || '+5511999999999';
const agentWallet = args.wallet || process.env.CHAINFX_AGENT_WALLET || DEFAULT_AGENT_WALLET;

const report = {
  started_at: new Date().toISOString(),
  card_url: cardUrl,
  objective: 'Discover ChainFX Agent Pay from Agent Card only and execute A2A payment flow.',
  inputs: {
    amount_brl: amountBRL,
    pix_key_configured: Boolean(pixKey),
    agent_wallet_configured: Boolean(agentWallet),
    bearer_configured: Boolean(bearer),
  },
  checks: {
    card_fetched: false,
    a2a_url_detected: false,
    skills_detected: false,
    payment_skill_selected: false,
    quote_called: false,
    auth_handled: false,
    intent_created: false,
    status_checked: false,
  },
  selected_skills: {},
  steps: [],
  completed: false,
};

function addStep(name, data) {
  report.steps.push({
    name,
    at: new Date().toISOString(),
    ...data,
  });
}

function redactHeaders(headers) {
  const copy = { ...headers };
  if (copy.Authorization) copy.Authorization = 'Bearer ***';
  return copy;
}

function safeJson(value) {
  if (value == null) return null;
  if (typeof value !== 'object') return value;
  return JSON.parse(JSON.stringify(value));
}

function compactBody(body) {
  if (body == null) return null;
  const serialized = JSON.stringify(body);
  if (serialized.length <= 4000) return body;
  return {
    truncated: true,
    preview: serialized.slice(0, 4000),
  };
}

async function requestJSON(url, options = {}) {
  const started = performance.now();
  const response = await fetch(url, {
    ...options,
    headers: {
      Accept: 'application/json',
      ...(options.body ? { 'Content-Type': 'application/json' } : {}),
      ...(options.headers || {}),
    },
  });
  const latencyMs = Math.round(performance.now() - started);
  const text = await response.text();
  let body = null;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = { raw: text };
    }
  }
  return {
    ok: response.ok,
    status: response.status,
    latency_ms: latencyMs,
    body,
  };
}

function normalizeSkill(skill) {
  const id = String(skill?.id || skill?.name || '').toLowerCase();
  const name = String(skill?.name || '').toLowerCase();
  const description = String(skill?.description || '').toLowerCase();
  const tags = Array.isArray(skill?.tags) ? skill.tags.map((tag) => String(tag).toLowerCase()) : [];
  return { id, name, description, tags };
}

function findSkill(skills, predicates) {
  for (const skill of skills) {
    const normalized = normalizeSkill(skill);
    if (predicates.some((predicate) => predicate(normalized))) {
      return skill.id || skill.name;
    }
  }
  return '';
}

function extractA2AUrl(card) {
  if (typeof card?.url === 'string' && card.url) return card.url;
  if (typeof card?.endpoints?.a2a === 'string' && card.endpoints.a2a) return card.endpoints.a2a;
  if (typeof card?.a2a === 'string' && card.a2a) return card.a2a;
  return '';
}

async function callA2A(a2aUrl, skill, payload, authMode = 'optional') {
  const headers = {};
  if (bearer) headers.Authorization = `Bearer ${bearer}`;
  const requestBody = {
    jsonrpc: '2.0',
    id: `agentqa-${Date.now()}-${Math.random().toString(16).slice(2)}`,
    skill,
    arguments: payload,
  };
  const response = await requestJSON(a2aUrl, {
    method: 'POST',
    headers,
    body: JSON.stringify(requestBody),
  });
  const unauthorized = response.status === 401 || response.status === 403;
  if (unauthorized && authMode === 'required') {
    report.checks.auth_handled = true;
  }
  addStep(`a2a:${skill}`, {
    request: {
      url: a2aUrl,
      method: 'POST',
      headers: redactHeaders(headers),
      body: requestBody,
    },
    response: {
      status: response.status,
      latency_ms: response.latency_ms,
      body: compactBody(response.body),
    },
  });
  return response;
}

function pickIntentId(body) {
  const candidates = [
    body?.result?.payment?.intent_id,
    body?.result?.payment?.id,
    body?.result?.payment?.payment_intent_id,
    body?.result?.intent_id,
    body?.result?.id,
    body?.payment?.intent_id,
    body?.payment?.id,
    body?.intent_id,
    body?.id,
  ];
  return candidates.find((value) => typeof value === 'string' && value.trim()) || '';
}

async function main() {
  try {
    const cardResponse = await requestJSON(cardUrl);
    addStep('fetch_agent_card', {
      request: { url: cardUrl, method: 'GET' },
      response: {
        status: cardResponse.status,
        latency_ms: cardResponse.latency_ms,
        body: compactBody(cardResponse.body),
      },
    });

    if (!cardResponse.ok || !cardResponse.body) {
      report.failed_at = 'fetch_agent_card';
      throw new Error(`Agent Card fetch failed with HTTP ${cardResponse.status}`);
    }

    report.checks.card_fetched = true;
    const card = cardResponse.body;
    const a2aUrl = extractA2AUrl(card);
    if (!a2aUrl) {
      report.failed_at = 'extract_a2a_url';
      throw new Error('Agent Card does not expose an A2A URL.');
    }
    report.a2a_url = a2aUrl;
    report.checks.a2a_url_detected = true;

    const skills = Array.isArray(card.skills) ? card.skills : [];
    report.discovered_skill_count = skills.length;
    report.discovered_skills = skills.map((skill) => skill.id || skill.name).filter(Boolean);
    report.checks.skills_detected = skills.length > 0;

    const skillMap = {
      methods: findSkill(skills, [
        (s) => s.id === 'list_supported_payment_methods',
        (s) => s.tags.includes('discovery') && s.tags.includes('payments'),
      ]),
      quote: findSkill(skills, [
        (s) => s.id === 'quote_required_usdt',
        (s) => s.id.includes('quote') && s.id.includes('usdt'),
      ]),
      payPix: findSkill(skills, [
        (s) => s.id === 'pay_pix_with_usdt',
        (s) => s.id.includes('pix') && s.id.includes('usdt'),
      ]),
      status: findSkill(skills, [
        (s) => s.id === 'get_payment_status',
        (s) => s.id.includes('status') && s.tags.includes('payments'),
      ]),
    };
    report.selected_skills = skillMap;
    report.checks.payment_skill_selected = Boolean(skillMap.payPix);

    if (!skillMap.methods || !skillMap.quote || !skillMap.payPix || !skillMap.status) {
      report.failed_at = 'select_required_skills';
      throw new Error(`Required skills were not discoverable: ${JSON.stringify(skillMap)}`);
    }

    await callA2A(a2aUrl, skillMap.methods, {}, 'optional');

    const quoteResponse = await callA2A(a2aUrl, skillMap.quote, {
      type: 'pix',
      amount_brl: amountBRL,
      agent_wallet: agentWallet,
    }, 'required');
    report.checks.quote_called = quoteResponse.ok;

    if (!quoteResponse.ok && !bearer && (quoteResponse.status === 401 || quoteResponse.status === 403)) {
      report.completed = false;
      report.outcome = 'discovery_ok_auth_required';
      report.failed_at = 'quote_required_auth';
      return;
    }

    if (!quoteResponse.ok) {
      report.failed_at = 'quote_required_usdt';
      throw new Error(`Quote failed with HTTP ${quoteResponse.status}`);
    }

    const createResponse = await callA2A(a2aUrl, skillMap.payPix, {
      amount_brl: amountBRL,
      pix_key: pixKey,
      beneficiary_name: 'ChainFX Agent QA',
      idempotency_key: `agentqa-${Date.now()}`,
      agent_wallet: agentWallet,
    }, 'required');

    if (!createResponse.ok && !bearer && (createResponse.status === 401 || createResponse.status === 403)) {
      report.completed = false;
      report.outcome = 'discovery_and_quote_ok_auth_required_for_payment';
      report.failed_at = 'payment_required_auth';
      return;
    }

    if (!createResponse.ok) {
      report.failed_at = 'pay_pix_with_usdt';
      throw new Error(`Payment intent creation failed with HTTP ${createResponse.status}`);
    }

    const intentId = pickIntentId(createResponse.body);
    report.payment_intent_id = intentId || null;
    report.checks.intent_created = Boolean(intentId);

    if (!intentId) {
      report.failed_at = 'extract_payment_intent_id';
      throw new Error('Payment intent was created but no intent id was found in the response.');
    }

    const statusResponse = await callA2A(a2aUrl, skillMap.status, { intent_id: intentId }, 'required');
    report.checks.status_checked = statusResponse.ok;

    if (!statusResponse.ok) {
      report.failed_at = 'get_payment_status';
      throw new Error(`Status check failed with HTTP ${statusResponse.status}`);
    }

    report.completed = true;
    report.outcome = 'agent_card_to_a2a_payment_flow_completed';
  } catch (error) {
    report.completed = false;
    report.error = {
      message: error instanceof Error ? error.message : String(error),
    };
  } finally {
    report.finished_at = new Date().toISOString();
    const output = `${JSON.stringify(safeJson(report), null, 2)}\n`;
    if (reportPath) {
      await writeFile(reportPath, output, 'utf8');
    }
    process.stdout.write(output);
    if (!report.completed && bearer) {
      process.exitCode = 2;
    }
  }
}

await main();
