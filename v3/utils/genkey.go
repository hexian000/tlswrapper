package utils

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/hexian000/gosnippets/formats"
)

const sni = "example.com"

func GenerateX509KeyPair(bits int, certFile, keyFile string) error {
	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return fmt.Errorf("RSA generate key: %s", formats.Error(err))
	}
	rawKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("PKCS8 private key: %s", formats.Error(err))
	}
	keyPem := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: rawKey,
	})

	now := time.Now()
	tmpl := x509.Certificate{
		NotBefore:    now,
		NotAfter:     now.AddDate(100, 0, 0),
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject: pkix.Name{
			Country:            []string{"US"},
			Province:           []string{"California"},
			Locality:           []string{"Mountain View"},
			Organization:       []string{"Your Organization"},
			OrganizationalUnit: []string{"Your Unit"},
			CommonName:         sni,
		},
		DNSNames: []string{sni},
	}
	rawCert, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("X.509: %s", formats.Error(err))
	}
	certPem := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: rawCert,
	})

	if err := os.WriteFile(certFile, certPem, 0644); err != nil {
		return fmt.Errorf("write certificate: %s", formats.Error(err))
	}
	if err := os.WriteFile(keyFile, keyPem, 0600); err != nil {
		return fmt.Errorf("write private key: %s", formats.Error(err))
	}
	return nil
}
