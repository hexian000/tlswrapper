// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"crypto/x509"
	"os"
	"testing"
)

func TestReadWriteKeyPairRoundTrip(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	gen, err := makeKeyGenerator("ecdsa", 256)
	if err != nil {
		t.Fatal(err)
	}
	pubKey, key, err := gen()
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := newCertificate(nil, nil, "peer.example.com", pubKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeKeyPair("peer", certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	cert, parsedKey, err := readKeyPair("peer")
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != "peer.example.com" {
		t.Fatalf("CommonName = %q, want %q", cert.Subject.CommonName, "peer.example.com")
	}
	if parsedKey == nil {
		t.Fatal("expected parsed private key")
	}
}

func TestGenCertsCreatesSignedCertificates(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	if code := genCerts(&AppFlags{GenCerts: "ca", ServerName: "ca.example.com", KeyType: "ed25519"}); code != 0 {
		t.Fatalf("genCerts(ca) = %d, want 0", code)
	}
	if code := genCerts(&AppFlags{GenCerts: "leaf-a,leaf-b", Sign: "ca", ServerName: "leaf.example.com", KeyType: "ecdsa", KeySize: 256}); code != 0 {
		t.Fatalf("genCerts(leaves) = %d, want 0", code)
	}
	caCert, _, err := readKeyPair("ca")
	if err != nil {
		t.Fatal(err)
	}
	leafCert, _, err := readKeyPair("leaf-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := readKeyPair("leaf-b"); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := leafCert.Verify(x509.VerifyOptions{Roots: pool, DNSName: "leaf.example.com"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ca-cert.pem", "ca-key.pem", "leaf-a-cert.pem", "leaf-a-key.pem", "leaf-b-cert.pem", "leaf-b-key.pem"} {
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("Stat(%q): %v", name, err)
		}
	}
}

func TestReadKeyPairErrors(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)

	t.Run("cert-file-missing", func(t *testing.T) {
		_, _, err := readKeyPair("missing")
		if err == nil {
			t.Fatal("expected error for missing cert file, got nil")
		}
	})

	t.Run("cert-no-certificate-block", func(t *testing.T) {
		if err := os.WriteFile("nocert-cert.pem", []byte("not pem\n"), 0644); err != nil {
			t.Fatal(err)
		}
		_, _, err := readKeyPair("nocert")
		if err == nil {
			t.Fatal("expected error when cert PEM has no CERTIFICATE block, got nil")
		}
	})

	t.Run("key-file-missing", func(t *testing.T) {
		// Write a valid cert file but no key file.
		gen, err := makeKeyGenerator("ed25519", 0)
		if err != nil {
			t.Fatal(err)
		}
		pubKey, key, err := gen()
		if err != nil {
			t.Fatal(err)
		}
		certPEM, _, err := newCertificate(nil, nil, "test.example.com", pubKey, key)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("nokey-cert.pem", certPEM, 0644); err != nil {
			t.Fatal(err)
		}
		_, _, err = readKeyPair("nokey")
		if err == nil {
			t.Fatal("expected error for missing key file, got nil")
		}
	})

	t.Run("key-no-private-key-block", func(t *testing.T) {
		gen, err := makeKeyGenerator("ed25519", 0)
		if err != nil {
			t.Fatal(err)
		}
		pubKey, key, err := gen()
		if err != nil {
			t.Fatal(err)
		}
		certPEM, _, err := newCertificate(nil, nil, "test.example.com", pubKey, key)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("badkey-cert.pem", certPEM, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("badkey-key.pem", []byte("not pem\n"), 0644); err != nil {
			t.Fatal(err)
		}
		_, _, err = readKeyPair("badkey")
		if err == nil {
			t.Fatal("expected error when key PEM has no PRIVATE KEY block, got nil")
		}
	})

	t.Run("key-invalid-pkcs8", func(t *testing.T) {
		gen, err := makeKeyGenerator("ed25519", 0)
		if err != nil {
			t.Fatal(err)
		}
		pubKey, key, err := gen()
		if err != nil {
			t.Fatal(err)
		}
		certPEM, _, err := newCertificate(nil, nil, "test.example.com", pubKey, key)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("badder-cert.pem", certPEM, 0644); err != nil {
			t.Fatal(err)
		}
		// A PEM block whose DER content is not a valid PKCS8 key.
		invalidKeyPEM := "-----BEGIN PRIVATE KEY-----\ndGhpcyBpcyBub3QgYSBrZXk=\n-----END PRIVATE KEY-----\n"
		if err := os.WriteFile("badder-key.pem", []byte(invalidKeyPEM), 0644); err != nil {
			t.Fatal(err)
		}
		_, _, err = readKeyPair("badder")
		if err == nil {
			t.Fatal("expected error for invalid PKCS8 DER, got nil")
		}
	})
}
