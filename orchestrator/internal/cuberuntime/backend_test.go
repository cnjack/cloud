package cuberuntime

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	cube "github.com/tencentcloud/CubeSandbox/sdk/go"
)

func TestSDKBackendRoutesEnvdThroughProxyWithoutSandboxDNS(t *testing.T) {
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer control-key" {
			t.Error("control credential missing")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"sandboxID":"routing-test","templateID":"tpl-test","domain":"cube.invalid","envdAccessToken":"envd-test","trafficAccessToken":"traffic-test"}`)
	}))
	defer control.Close()
	data := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "49983-routing-test.cube.invalid" {
			t.Errorf("virtual Host=%q", r.Host)
		}
		if r.URL.Path != "/process.Process/Start" {
			t.Errorf("RPC path=%q", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "Bearer control-key" {
			t.Error("control credential leaked to guest")
		}
		frame := func(flag byte, payload string) {
			header := make([]byte, 5)
			header[0] = flag
			binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
			w.Write(header)
			w.Write([]byte(payload))
		}
		w.Header().Set("Content-Type", "application/connect+json")
		frame(0, `{"event":{"start":{"pid":1}}}`)
		frame(0, fmt.Sprintf(`{"event":{"data":{"stdout":%q}}}`, base64.StdEncoding.EncodeToString([]byte("route-ok\n"))))
		frame(0, `{"event":{"end":{"exitCode":0,"exited":true,"status":"exited"}}}`)
		frame(2, `{}`)
	}))
	defer data.Close()
	u, _ := url.Parse(data.URL)
	host, port, _ := net.SplitHostPort(u.Host)
	number, _ := strconv.Atoi(port)
	backend := NewSDKBackend(cube.Config{APIURL: control.URL, APIKey: "control-key", ProxyNodeIP: host, ProxyPortHTTP: number, ProxyScheme: "http", SandboxDomain: "cube.invalid", RequestTimeout: time.Second})
	ctx := context.Background()
	vm, err := backend.Create(ctx, "tpl-test", map[string]string{"jcloud.owner": "test"})
	if err != nil {
		t.Fatal(err)
	}
	output, err := vm.Run(ctx, "echo route-ok", time.Second)
	if err != nil || output != "route-ok\n" {
		t.Fatalf("data-plane execution: output=%q error=%v", output, err)
	}
}

func TestSDKBackendLive(t *testing.T) {
	if os.Getenv("CUBE_SDK_LIVE") != "1" {
		t.Skip("CUBE_SDK_LIVE=1 enables the real adapter lifecycle test")
	}
	cfg := cube.NewConfigFromEnv()
	backend := NewSDKBackend(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	vm, err := backend.Create(ctx, cfg.TemplateID, map[string]string{"jcloud.owner": "poc-sdk-adapter"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := backend.Delete(ctx, vm.ID()); err != nil {
			t.Error(err)
		}
	})
	if err := vm.Write(ctx, "/tmp/jcloud-adapter-proof", []byte("adapter-proof\n")); err != nil {
		t.Fatal(err)
	}
	output, err := vm.Run(ctx, "cat /tmp/jcloud-adapter-proof", 20*time.Second)
	if err != nil || output != "adapter-proof\n" {
		t.Fatalf("real adapter: output=%q error=%v", output, err)
	}
}
