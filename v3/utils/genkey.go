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

func GenerateX509KeyPair(bits int, certFile, keyFile string) error {
	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return fmt.Errorf("generate RSA private key: %s", formats.Error(err))
	}
	rawKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal PKCS8 private key: %s", formats.Error(err))
	}
	keyPem := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: rawKey,
	})

	now := time.Now()
	tml := x509.Certificate{
		NotBefore:    now,
		NotAfter:     now.AddDate(100, 0, 0),
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject: pkix.Name{
			CommonName:         "example.com",
			Organization:       []string{"Your Organization"},
			OrganizationalUnit: []string{"Your Unit"},
		},
		BasicConstraintsValid: true,
	}
	rawCert, err := x509.CreateCertificate(rand.Reader, &tml, &tml, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create x509 certificate: %s", formats.Error(err))
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
