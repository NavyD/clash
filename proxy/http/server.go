package http

import (
	"bufio"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
	"time"

	adapters "github.com/Dreamacro/clash/adapters/inbound"
	"github.com/Dreamacro/clash/common/cache"
	"github.com/Dreamacro/clash/component/auth"
	"github.com/Dreamacro/clash/log"
	authStore "github.com/Dreamacro/clash/proxy/auth"
	"github.com/Dreamacro/clash/tunnel"
)

type HTTPListener struct {
	net.Listener
	address string
	closed  bool
	cache   *cache.Cache
}

// 在指定addr上监听tcp并创建一个新的goroutine处理http数据 
// 如果http.accept出现错误，只有当http.closed时会退出监听，否则
// 忽略错误
func NewHTTPProxy(addr string) (*HTTPListener, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	hl := &HTTPListener{l, addr, false, cache.New(30 * time.Second)}

	go func() {
		log.Infoln("HTTP proxy listening at: %s", addr)

		for {
			c, err := hl.Accept()
			if err != nil {
				if hl.closed {
					break
				}
				continue
			}
			go HandleConn(c, hl.cache)
		}
	}()

	return hl, nil
}

func (l *HTTPListener) Close() {
	l.closed = true
	l.Listener.Close()
}

func (l *HTTPListener) Address() string {
	return l.address
}

func canActivate(loginStr string, authenticator auth.Authenticator, cache *cache.Cache) (ret bool) {
	if result := cache.Get(loginStr); result != nil {
		ret = result.(bool)
		return
	}
	loginData, err := base64.StdEncoding.DecodeString(loginStr)
	login := strings.Split(string(loginData), ":")
	ret = err == nil && len(login) == 2 && authenticator.Verify(login[0], login[1])

	cache.Put(loginStr, ret, time.Minute)
	return
}

// 处理http连接：用户认证、响应http connect方法并接收
// http request到tunel channel中处理
//
// 如果request url.host为空时终止处理
func HandleConn(conn net.Conn, cache *cache.Cache) {
	br := bufio.NewReader(conn)
	// label 类似c中的goto，可用于跳转  与变量名不冲突，但是存在同名变量时
	// 不规范
keepAlive:
	request, err := http.ReadRequest(br)
	if err != nil || request.URL.Host == "" {
		conn.Close()
		return
	}

	keepAlive := strings.TrimSpace(strings.ToLower(request.Header.Get("Proxy-Connection"))) == "keep-alive"
	authenticator := authStore.Authenticator()
	if authenticator != nil {
		if authStrings := strings.Split(request.Header.Get("Proxy-Authorization"), " "); len(authStrings) != 2 {
			conn.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic\r\n\r\n"))
			if keepAlive {
				goto keepAlive
			}
			return
		} else if !canActivate(authStrings[1], authenticator, cache) {
			conn.Write([]byte("HTTP/1.1 403 Forbidden\r\n\r\n"))
			log.Infoln("Auth failed from %s", conn.RemoteAddr().String())
			if keepAlive {
				goto keepAlive
			}
			conn.Close()
			return
		}
	}

	// http connnect 回复建立代理
	if request.Method == http.MethodConnect {
		_, err := conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		if err != nil {
			conn.Close()
			return
		}
		tunnel.Add(adapters.NewHTTPS(request, conn))
		return
	}

	tunnel.Add(adapters.NewHTTP(request, conn))
}
