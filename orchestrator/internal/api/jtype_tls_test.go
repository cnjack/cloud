package api

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestJTypeCertificateFailureIsActionableAndDoesNotLeakEndpoint(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "https://private.internal/board", Err: &tls.CertificateVerificationError{Err: x509.CertificateInvalidError{Cert: &x509.Certificate{}, Reason: x509.Expired}}}
	w := httptest.NewRecorder()
	if !writeJTypeTLSFailure(w, err) || w.Code != 503 {
		t.Fatal("certificate failure was not classified")
	}
	body := w.Body.String()
	if !strings.Contains(body, "jtype_tls_invalid") || !strings.Contains(body, "administrator") || strings.Contains(body, "private.internal") {
		t.Fatalf("unexpected response: %s", body)
	}
	if writeJTypeTLSFailure(httptest.NewRecorder(), errors.New("network down")) {
		t.Fatal("network outage misclassified as certificate failure")
	}
}
