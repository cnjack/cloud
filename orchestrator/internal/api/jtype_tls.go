package api

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
)

// Certificate failures require operator repair, not account reauthorization.
// Keep upstream addresses and certificate details in logs rather than exposing
// them to every Repository member through the board proxy.
func writeJTypeTLSFailure(w http.ResponseWriter, err error) bool {
	var verification *tls.CertificateVerificationError
	var invalid x509.CertificateInvalidError
	var authority x509.UnknownAuthorityError
	if !errors.As(err, &verification) && !errors.As(err, &invalid) && !errors.As(err, &authority) {
		return false
	}
	writeError(w, http.StatusServiceUnavailable, "jtype_tls_invalid", "JType's HTTPS certificate could not be verified. Ask the administrator to renew or repair the certificate, then retry.")
	return true
}
