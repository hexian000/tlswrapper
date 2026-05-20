// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"crypto/x509"
	"testing"
)

func TestMakeKeyGenerator(t *testing.T) {
	tests := []struct {
		name    string
		keytype string
		keysize int
		wantErr bool
	}{
		{name: "ecdsa-default", keytype: "ecdsa", keysize: 0},
		{name: "ecdsa-p224", keytype: "ecdsa", keysize: 224},
		{name: "ecdsa-p256", keytype: "ecdsa", keysize: 256},
		{name: "ecdsa-p384", keytype: "ecdsa", keysize: 384},
		{name: "ecdsa-p521", keytype: "ecdsa", keysize: 521},
		{name: "ecdsa-invalid-size", keytype: "ecdsa", keysize: 512, wantErr: true},
		{name: "ed25519", keytype: "ed25519", keysize: 0},
		{name: "invalid-keytype", keytype: "dsa", keysize: 0, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gen, err := makeKeyGenerator(tt.keytype, tt.keysize)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if gen == nil {
				t.Fatal("expected non-nil generator")
			}
			// Call the generator and verify it produces a usable key pair.
			pubKey, key, err := gen()
			if err != nil {
				t.Fatal("generator failed:", err)
			}
			if pubKey == nil || key == nil {
				t.Fatal("generator returned nil key material")
			}
		})
	}
}

// TestMakeKeyGeneratorRSA is kept separate because RSA key generation is slow.
func TestMakeKeyGeneratorRSA(t *testing.T) {
	gen, err := makeKeyGenerator("rsa", 0)
	if err != nil {
		t.Fatal(err)
	}
	if gen == nil {
		t.Fatal("expected non-nil generator for rsa")
	}
	// We only verify that the generator is non-nil; actually generating a
	// 4096-bit RSA key in a unit test is too slow.
}

// TestMakeKeyGeneratorRSASmall uses a 1024-bit key so the test finishes quickly
// while still exercising the RSA generator function body.
func TestMakeKeyGeneratorRSASmall(t *testing.T) {
	gen, err := makeKeyGenerator("rsa", 1024)
	if err != nil {
		t.Fatal(err)
	}
	pubKey, key, err := gen()
	if err != nil {
		t.Fatal("rsa generator failed:", err)
	}
	if pubKey == nil || key == nil {
		t.Fatal("rsa generator returned nil key material")
	}
}

func TestNewCertificate(t *testing.T) {
	t.Run("self-signed", func(t *testing.T) {
		gen, err := makeKeyGenerator("ecdsa", 256)
		if err != nil {
			t.Fatal(err)
		}
		pubKey, key, err := gen()
		if err != nil {
			t.Fatal(err)
		}

		certPEM, keyPEM, err := newCertificate(nil, nil, "test.example.com", pubKey, key)
		if err != nil {
			t.Fatal(err)
		}
		if len(certPEM) == 0 {
			t.Fatal("certPEM is empty")
		}
		if len(keyPEM) == 0 {
			t.Fatal("keyPEM is empty")
		}

		// Verify the generated certificate is parseable.
		certDER := parsePEM(certPEM, "CERTIFICATE")
		if certDER == nil {
			t.Fatal("parsePEM returned nil for CERTIFICATE block")
		}
		cert, err := x509.ParseCertificate(certDER)
		if err != nil {
			t.Fatal("x509.ParseCertificate:", err)
		}
		if cert.Subject.CommonName != "test.example.com" {
			t.Fatalf("CommonName = %q, want %q", cert.Subject.CommonName, "test.example.com")
		}
		if !cert.IsCA {
			t.Fatal("self-signed cert should have IsCA=true")
		}
	})

	t.Run("signed-by-parent", func(t *testing.T) {
		// Generate CA key pair.
		caGen, err := makeKeyGenerator("ed25519", 0)
		if err != nil {
			t.Fatal(err)
		}
		caPubKey, caKey, err := caGen()
		if err != nil {
			t.Fatal(err)
		}
		caCertPEM, _, err := newCertificate(nil, nil, "ca.example.com", caPubKey, caKey)
		if err != nil {
			t.Fatal(err)
		}

		// Parse CA cert to use as parent.
		caDER := parsePEM(caCertPEM, "CERTIFICATE")
		if caDER == nil {
			t.Fatal("could not find CERTIFICATE block in CA PEM")
		}
		caCert, err := x509.ParseCertificate(caDER)
		if err != nil {
			t.Fatal(err)
		}

		// Generate leaf key pair and sign with CA.
		leafGen, err := makeKeyGenerator("ecdsa", 256)
		if err != nil {
			t.Fatal(err)
		}
		leafPubKey, leafKey, err := leafGen()
		if err != nil {
			t.Fatal(err)
		}
		certPEM, keyPEM, err := newCertificate(caCert, caKey, "leaf.example.com", leafPubKey, leafKey)
		if err != nil {
			t.Fatal(err)
		}
		if len(certPEM) == 0 || len(keyPEM) == 0 {
			t.Fatal("empty PEM output")
		}

		// Verify the leaf certificate is parseable and has the correct CN.
		certDER := parsePEM(certPEM, "CERTIFICATE")
		leafCert, err := x509.ParseCertificate(certDER)
		if err != nil {
			t.Fatal(err)
		}
		if leafCert.Subject.CommonName != "leaf.example.com" {
			t.Fatalf("CommonName = %q, want %q", leafCert.Subject.CommonName, "leaf.example.com")
		}
		// Leaf cert should not be a CA.
		if leafCert.IsCA {
			t.Fatal("leaf cert should not be a CA")
		}

		// Verify that the leaf is signed by the CA.
		pool := x509.NewCertPool()
		pool.AddCert(caCert)
		opts := x509.VerifyOptions{Roots: pool, DNSName: "leaf.example.com"}
		if _, err := leafCert.Verify(opts); err != nil {
			t.Fatal("leaf cert does not verify against CA:", err)
		}
	})
}

func TestParsePEM(t *testing.T) {
	gen, err := makeKeyGenerator("ed25519", 0)
	if err != nil {
		t.Fatal(err)
	}
	pubKey, key, err := gen()
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := newCertificate(nil, nil, "parse.example.com", pubKey, key)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("extract-certificate", func(t *testing.T) {
		der := parsePEM(certPEM, "CERTIFICATE")
		if der == nil {
			t.Fatal("expected non-nil DER for CERTIFICATE block")
		}
		if _, err := x509.ParseCertificate(der); err != nil {
			t.Fatal("parsed DER is not a valid certificate:", err)
		}
	})

	t.Run("extract-private-key", func(t *testing.T) {
		der := parsePEM(keyPEM, "PRIVATE KEY")
		if der == nil {
			t.Fatal("expected non-nil DER for PRIVATE KEY block")
		}
		if _, err := x509.ParsePKCS8PrivateKey(der); err != nil {
			t.Fatal("parsed DER is not a valid PKCS8 private key:", err)
		}
	})

	t.Run("block-not-found", func(t *testing.T) {
		der := parsePEM(certPEM, "NONEXISTENT")
		if der != nil {
			t.Fatal("expected nil DER when block type is absent")
		}
	})

	t.Run("non-pem-data", func(t *testing.T) {
		der := parsePEM([]byte("not pem data at all"), "CERTIFICATE")
		if der != nil {
			t.Fatal("expected nil DER for non-PEM input")
		}
	})

	t.Run("skip-wrong-type", func(t *testing.T) {
		// keyPEM contains only PRIVATE KEY blocks; querying for CERTIFICATE should return nil.
		der := parsePEM(keyPEM, "CERTIFICATE")
		if der != nil {
			t.Fatal("expected nil DER when CERTIFICATE block not in key PEM")
		}
	})
}
