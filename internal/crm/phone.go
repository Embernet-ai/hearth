package crm

import "strings"

// PhoneE164 is migrations/00059's phone_e164 in Go, so a number asked about from the API is normalized the
// same way the index was built. A leading + or 00 keeps the country code that was typed; ten digits, or
// eleven starting with 1, are North American; anything else is ambiguous and returns "", which matches
// nothing. Keep the two in step: crm_integration_test checks them against each other.
func PhoneE164(p string) string {
	var d strings.Builder
	for _, r := range p {
		if r >= '0' && r <= '9' {
			d.WriteRune(r)
		}
	}
	digits := d.String()
	plus := strings.HasPrefix(strings.TrimSpace(p), "+")
	switch {
	case digits == "":
		return ""
	case plus && len(digits) >= 8 && len(digits) <= 15:
		return "+" + digits
	case strings.HasPrefix(digits, "00") && len(digits) >= 10 && len(digits) <= 17:
		return "+" + digits[2:]
	case len(digits) == 10:
		return "+1" + digits
	case len(digits) == 11 && digits[0] == '1':
		return "+" + digits
	}
	return ""
}
