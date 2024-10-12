package tlswrapper

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v3/config"
)

func importCert(inCfg, outCfg string) error {
	cfg, err := config.LoadFile(inCfg)
	if err != nil {
		return fmt.Errorf("load config: %s", formats.Error(err))
	}
	b, err := cfg.Dump()
	if err != nil {
		return fmt.Errorf("dump config: %s", formats.Error(err))
	}
	err = os.WriteFile(outCfg, b, 0600)
	if err != nil {
		return fmt.Errorf("write config: %s", formats.Error(err))
	}
	return nil
}

func ImportCert() {
	f := &Flags
	err := importCert(f.Config, f.ImportCert)
	if err != nil {
		slog.Fatal("importcert: ", formats.Error(err))
		os.Exit(1)
	}
	slog.Info("importcert: ok")
}

func generateX509KeyPair(sni string, bits int, certFile, keyFile string) error {
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

func GenCerts() {
	f := &Flags
	bits := f.KeySize
	for _, name := range strings.Split(f.GenCerts, ",") {
		certFile, keyFile := name+"-cert.pem", name+"-key.pem"
		slog.Infof("gencerts: %q (RSA %d bits)...", name, bits)
		err := generateX509KeyPair(f.ServerName, bits, certFile, keyFile)
		if err != nil {
			slog.Fatal("gencerts: ", formats.Error(err))
		}
		slog.Infof("gencerts: X.509 Certificate=%q, Private Key=%q", certFile, keyFile)
	}
}
