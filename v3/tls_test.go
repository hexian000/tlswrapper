package tlswrapper_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"testing"
)

func TestTLS(t *testing.T) {
	println("tls: start")
	serverCert, err := tls.X509KeyPair([]byte(testServerCertPEM), []byte(testServerKeyPEM))
	if err != nil {
		t.Error(err)
		return
	}
	clientCert, err := tls.X509KeyPair([]byte(testClientCertPEM), []byte(testClientKeyPEM))
	if err != nil {
		t.Error(err)
		return
	}
	serverCertPool := x509.NewCertPool()
	if ok := serverCertPool.AppendCertsFromPEM([]byte(testClientCertPEM)); !ok {
		t.Fail()
		return
	}
	clientCertPool := x509.NewCertPool()
	if ok := clientCertPool.AppendCertsFromPEM([]byte(testServerCertPEM)); !ok {
		t.Fail()
		return
	}
	serverCfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    serverCertPool,
		RootCAs:      serverCertPool,
		ServerName:   "example.com",
		MinVersion:   tls.VersionTLS13,
	}
	clientCfg := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCertPool,
		RootCAs:      clientCertPool,
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			block := &pem.Block{
				Type:  "CERTIFICATE",
				Bytes: rawCerts[0],
			}
			t.Logf("%s", string(pem.EncodeToMemory(block)))
			return nil
		},
		ServerName: "example.com",
		MinVersion: tls.VersionTLS13,
	}

	println("tls: listen")
	l, err := tls.Listen("tcp", "127.0.0.1:46854", serverCfg)
	if err != nil {
		panic(err)
	}
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		conn, err := l.Accept()
		if err != nil {
			panic(err)
		}
		b := make([]byte, 256)
		n, _ := conn.Read(b)
		println("tls: echo")
		_, _ = conn.Write(b[:n])
		_ = conn.Close()
	}()
	println("tls: dial")
	clientConn, err := tls.Dial("tcp", "127.0.0.1:46854", clientCfg)
	if err != nil {
		t.Error(err)
		return
	}
	println("tls: handshake")
	_, err = clientConn.Write([]byte("Hello TLS"))
	if err != nil {
		t.Error(err)
		return
	}
	b := make([]byte, 256)
	_, err = clientConn.Read(b)
	if err != nil {
		t.Error(err)
		return
	}
	_ = clientConn.Close()
	println("tls: ok")
}

const (
	testServerCertPEM = `-----BEGIN CERTIFICATE-----
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
tg==
-----END CERTIFICATE-----
`

	testServerKeyPEM = `-----BEGIN PRIVATE KEY-----
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
K8wzuiuQ06NyMgsYLd7aOEIZKMtMY8ko
-----END PRIVATE KEY-----
`
	testClientCertPEM = `-----BEGIN CERTIFICATE-----
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
5Q==
-----END CERTIFICATE-----
`

	testClientKeyPEM = `-----BEGIN PRIVATE KEY-----
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
P/ZsTO2FFDKwybPB4iSPJz60W5xxYcY=
-----END PRIVATE KEY-----
`
)
