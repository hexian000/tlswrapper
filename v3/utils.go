package tlswrapper

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync"
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
	slog.Notice("importcert: ok")
}

type keyGenerator func() (pubKey any, key any, err error)

func newCertificate(parent *x509.Certificate, signKey any, sni string, pubKey any, key any) (certPEM []byte, keyPEM []byte, err error) {
	rawKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		err = fmt.Errorf("PKCS8 private key: %s", formats.Error(err))
		return
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{
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
	if parent == nil {
		// create self-signed certificate
		parent = &tmpl
		tmpl.BasicConstraintsValid = true
		tmpl.IsCA = true
		signKey = key
	}
	rawCert, err := x509.CreateCertificate(rand.Reader, &tmpl, parent, pubKey, signKey)
	if err != nil {
		err = fmt.Errorf("X.509: %s", formats.Error(err))
		return
	}
	certPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: rawCert,
	})
	return
}

func makeKeyGenerator(keytype string, keysize int) (keyGenerator, error) {
	switch keytype {
	case "rsa":
		bits := keysize
		if bits == 0 {
			bits = 3072
		}
		slog.Noticef("gencerts: keytype=%q keysize=%d", keytype, bits)
		return func() (any, any, error) {
			key, err := rsa.GenerateKey(rand.Reader, bits)
			if err != nil {
				return nil, nil, err
			}
			return &key.PublicKey, key, nil
		}, nil
	case "ecdsa":
		if keysize == 0 {
			keysize = 256
		}
		var curve elliptic.Curve
		switch keysize {
		case 224:
			curve = elliptic.P224()
		case 256:
			curve = elliptic.P256()
		case 384:
			curve = elliptic.P384()
		case 521:
			curve = elliptic.P521()
		default:
			return nil, errors.New("invalid key size")
		}
		slog.Noticef("gencerts: keytype=%q keysize=%d", keytype, keysize)
		return func() (any, any, error) {
			key, err := ecdsa.GenerateKey(curve, rand.Reader)
			if err != nil {
				return nil, nil, err
			}
			return &key.PublicKey, key, err
		}, nil
	case "ed25519":
		slog.Noticef("gencerts: keytype=%q", keytype)
		return func() (any, any, error) {
			pubKey, key, err := ed25519.GenerateKey(rand.Reader)
			return pubKey, key, err
		}, nil
	}
	return nil, errors.New("invalid key type")
}

func savePEM(name string, certPem, keyPem []byte) error {
	certFile, keyFile := name+"-cert.pem", name+"-key.pem"
	if err := os.WriteFile(certFile, certPem, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(keyFile, keyPem, 0600); err != nil {
		return err
	}
	slog.Noticef("gencerts: %q, %q", certFile, keyFile)
	return nil
}

func genCA(f *AppFlags, keygen keyGenerator) (*x509.Certificate, any, error) {
	caPubKey, caKey, err := keygen()
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %s", formats.Error(err))
	}
	caCertPEM, caKeyPEM, err := newCertificate(nil, nil, f.ServerName, caPubKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	if err := savePEM("ca", caCertPEM, caKeyPEM); err != nil {
		return nil, nil, err
	}
	caDER, _ := pem.Decode(caCertPEM)
	if caDER == nil || caDER.Type != "CERTIFICATE" {
		return nil, nil, errors.New("error decoding CA")
	}
	caCert, err := x509.ParseCertificate(caDER.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return caCert, caKey, nil
}

func GenCerts() {
	f := &Flags
	wg := &sync.WaitGroup{}
	keygen, err := makeKeyGenerator(f.KeyType, f.KeySize)
	if err != nil {
		slog.Errorf("gencerts: %s", formats.Error(err))
		return
	}
	caCert, caKey, err := genCA(f, keygen)
	if err != nil {
		slog.Errorf("gencerts: CA: %s", formats.Error(err))
		return
	}
	for _, name := range strings.Split(f.GenCerts, ",") {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			pubKey, key, err := keygen()
			if err != nil {
				slog.Errorf("generate key: %s", formats.Error(err))
				return
			}
			certPem, keyPem, err := newCertificate(caCert, caKey, f.ServerName, pubKey, key)
			if err != nil {
				slog.Errorf("gencerts %q: %s", name, formats.Error(err))
				return
			}
			if err := savePEM(name, certPem, keyPem); err != nil {
				slog.Errorf("gencerts: %s", formats.Error(err))
				return
			}
		}(name)
	}
	wg.Wait()
	slog.Notice("gencerts: ok")
}
