package mobile

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"payment-gateway/internal/models"
)

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func decodeJSON(r *http.Request, dest any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(dest)
}

func marshalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

func itoa(i int) string {
	return strconv.Itoa(i)
}

// sanitizeUser removes sensitive fields before sending to client.
func sanitizeUser(u *models.User) map[string]any {
	out := map[string]any{
		"id":                 u.ID,
		"email":              u.Email,
		"phone":              u.Phone,
		"full_name":          u.FullName,
		"avatar_url":         u.AvatarURL,
		"wallet_address":     u.WalletAddress,
		"pix_key":            u.PixKey,
		"kyc_status":         u.KYCStatus,
		"biometry_enabled":   u.BiometryEnabled,
		"two_factor_enabled": u.TwoFactorEnabled,
		"created_at":         u.CreatedAt,
	}
	if cpf := mobileUserCPF(u); cpf != "" {
		out["cpf"] = cpf
		out["document_number"] = cpf
	}
	return out
}

func sanitizeUserForMobile(u *models.User, adminBootstrapEmail string) map[string]any {
	out := sanitizeUser(u)
	adminEmail := strings.ToLower(strings.TrimSpace(adminBootstrapEmail))
	userEmail := strings.ToLower(strings.TrimSpace(u.Email))
	out["is_developer"] = adminEmail != "" && userEmail == adminEmail
	return out
}

func mobileUserString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func mobileUserCPF(u *models.User) string {
	if u == nil || u.KYCDocuments == nil {
		return ""
	}
	var docs map[string]any
	if err := json.Unmarshal([]byte(*u.KYCDocuments), &docs); err != nil {
		return ""
	}
	for _, key := range []string{"cpf", "number", "document_number", "documentNumber"} {
		if value, ok := docs[key]; ok {
			if cpf := onlyDigitsMobile(value); cpf != "" {
				return cpf
			}
		}
	}
	return ""
}

func onlyDigitsMobile(value any) string {
	raw := strings.TrimSpace(valueToString(value))
	if raw == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func valueToString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}
