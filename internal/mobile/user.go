package mobile

import (
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

func (s *Server) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	user, err := mobileDB(s.db).GetUserByID(r.Context(), uid)
	if err != nil || user == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "usuário não encontrado"})
		return
	}
	writeJSON(w, http.StatusOK, sanitizeUser(user))
}

func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		FullName string `json:"full_name"`
		Phone    string `json:"phone"`
		PixKey   string `json:"pix_key"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload inválido"})
		return
	}
	fields := map[string]any{}
	if req.FullName != "" {
		fields["full_name"] = req.FullName
	}
	if req.Phone != "" {
		fields["phone"] = req.Phone
	}
	if req.PixKey != "" {
		fields["pix_key"] = req.PixKey
	}
	if len(fields) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "nenhum campo para atualizar"})
		return
	}
	if err := mobileDB(s.db).UpdateUser(r.Context(), uid, fields); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	user, _ := mobileDB(s.db).GetUserByID(r.Context(), uid)
	writeJSON(w, http.StatusOK, sanitizeUser(user))
}

// handleDeleteAccount soft-deletes and anonymizes the authenticated mobile account.
func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invalido"})
		return
	}
	if strings.TrimSpace(req.Password) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "password obrigatorio para excluir a conta"})
		return
	}
	user, err := mobileDB(s.db).GetUserByID(r.Context(), uid)
	if err != nil || user == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "usuario nao encontrado"})
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "password incorreto"})
		return
	}
	if err := mobileDB(s.db).DeleteUserAccount(r.Context(), uid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"deleted": true,
		"mode":    "soft_delete_anonymized",
	})
}

func (s *Server) handleSubmitKYC(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		DocumentType   string `json:"document_type"`
		DocumentNumber string `json:"document_number"`
		DocumentFront  string `json:"document_front_base64"`
		DocumentBack   string `json:"document_back_base64"`
		Selfie         string `json:"selfie_base64"`
	}
	if err := decodeJSON(r, &req); err != nil || req.DocumentType == "" || req.DocumentNumber == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "document_type e document_number obrigatórios"})
		return
	}
	docs, _ := marshalJSON(map[string]any{
		"type":       req.DocumentType,
		"number":     req.DocumentNumber,
		"has_front":  req.DocumentFront != "",
		"has_back":   req.DocumentBack != "",
		"has_selfie": req.Selfie != "",
	})
	docsStr := string(docs)
	if err := mobileDB(s.db).UpdateUser(r.Context(), uid, map[string]any{
		"kyc_status":    "submitted",
		"kyc_documents": docsStr,
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "kyc_status": "submitted"})
}

func (s *Server) handleKYCStatus(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	user, err := mobileDB(s.db).GetUserByID(r.Context(), uid)
	if err != nil || user == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "usuário não encontrado"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"kyc_status":    user.KYCStatus,
		"kyc_documents": user.KYCDocuments,
	})
}

// handleSetPIN — POST /api/mobile/security/pin
func (s *Server) handleSetPIN(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		PIN        string `json:"pin"`
		CurrentPIN string `json:"current_pin"`
	}
	if err := decodeJSON(r, &req); err != nil || len(req.PIN) < 4 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "pin deve ter no mínimo 4 dígitos"})
		return
	}
	user, _ := mobileDB(s.db).GetUserByID(r.Context(), uid)
	if user.PinHash != nil && *user.PinHash != "" {
		if req.CurrentPIN == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "current_pin obrigatório para alterar o PIN"})
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(*user.PinHash), []byte(req.CurrentPIN)); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "PIN atual incorreto"})
			return
		}
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(req.PIN), bcrypt.MinCost)
	_ = mobileDB(s.db).UpdateUser(r.Context(), uid, map[string]any{"pin_hash": string(hash)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSetBiometry — POST /api/mobile/security/biometry
func (s *Server) handleSetBiometry(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload inválido"})
		return
	}
	_ = mobileDB(s.db).UpdateUser(r.Context(), uid, map[string]any{"biometry_enabled": req.Enabled})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "biometry_enabled": req.Enabled})
}

// handleSet2FA — POST /api/mobile/security/2fa
func (s *Server) handleSet2FA(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		Enabled bool   `json:"enabled"`
		Code    string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload inválido"})
		return
	}
	secret := randomHex(10)
	fields := map[string]any{"two_factor_enabled": req.Enabled}
	if req.Enabled {
		fields["two_factor_secret"] = secret
	}
	_ = mobileDB(s.db).UpdateUser(r.Context(), uid, fields)
	resp := map[string]any{"ok": true, "two_factor_enabled": req.Enabled}
	if req.Enabled {
		resp["setup_secret"] = secret // client shows as QR for TOTP app
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleListDevices — GET /api/mobile/security/devices
func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	devices, err := mobileDB(s.db).ListDevices(r.Context(), uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

// handleRemoveDevice — DELETE /api/mobile/security/device
func (s *Server) handleRemoveDevice(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		DeviceID string `json:"device_id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.DeviceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "device_id obrigatório"})
		return
	}
	if err := mobileDB(s.db).DeleteDevice(r.Context(), uid, req.DeviceID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
