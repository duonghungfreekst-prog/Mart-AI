package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const (
	cdpViewportWidth  = 960
	cdpViewportHeight = 560
	cdpDefaultUA      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

const cdpStealthAndPopupHookJS = `
(function() {
  try {
    Object.defineProperty(navigator, 'webdriver', { get: function() { return undefined; } });
  } catch (_) {}
  window.open = function(url) {
    if (url) { location.href = url; }
    return window;
  };
  document.addEventListener('click', function(ev) {
    var a = ev.target && ev.target.closest ? ev.target.closest('a[target="_blank"]') : null;
    if (a && a.href) { a.removeAttribute('target'); }
  }, true);
})();
`

const cdpMuseSnapshotJS = `
(function() {
  var standardFormattingTags = {
    "p":1,"div":1,"span":1,"h1":1,"h2":1,"h3":1,"h4":1,"h5":1,"h6":1,
    "strong":1,"b":1,"em":1,"i":1,"u":1,"s":1,"a":1,"ul":1,"ol":1,"li":1,
    "table":1,"thead":1,"tbody":1,"tfoot":1,"tr":1,"th":1,"td":1,"blockquote":1,
    "pre":1,"code":1,"img":1,"video":1,"source":1,"audio":1,"iframe":1,
    "br":1,"hr":1,"button":1,"svg":1,"style":1,"script":1,"input":1,"textarea":1
  };

  function nodeToMd(n, listDepth) {
    if (!n) return "";
    if (n.nodeType === 3) return n.textContent || "";
    if (n.nodeType !== 1) return "";
    var tag = (n.tagName || "").toLowerCase();
    if (tag === "button" || tag === "svg" || tag === "style" || tag === "script") return "";

    // 1. Khối mã lệnh: Giữ nguyên thụt dòng và nhận diện chính xác XML, HTML, JSON, Go, Python, v.v.
    if (tag === "pre") {
      var codeEl = n.querySelector("code");
      var lang = "";
      if (codeEl && codeEl.className) {
        var m = codeEl.className.match(/language-([a-zA-Z0-9_+-]+)/i);
        if (m) lang = m[1].toLowerCase();
      }
      var rawCode = (codeEl ? codeEl.textContent : n.textContent) || "";
      if (!lang) {
        var trimmed = rawCode.trim();
        if (trimmed.startsWith("<?xml") || (trimmed.startsWith("<") && trimmed.endsWith(">") && trimmed.indexOf("</") > 0)) {
          if (trimmed.toLowerCase().startsWith("<!doctype html") || trimmed.toLowerCase().startsWith("<html")) {
            lang = "html";
          } else {
            lang = "xml";
          }
        } else if ((trimmed.startsWith("{") && trimmed.endsWith("}")) || (trimmed.startsWith("[") && trimmed.endsWith("]"))) {
          lang = "json";
        }
      }
      return "\n\x60\x60\x60" + lang + "\n" + rawCode.replace(/\n$/, "") + "\n\x60\x60\x60\n\n";
    }

    if (tag === "code") return "\x60" + (n.textContent || "") + "\x60";

    // 2. Video Player & Media: Thu thập đầy đủ link video, source và iframe embed
    if (tag === "video") {
      var vSrc = n.getAttribute("src") || n.currentSrc || n.src || "";
      if (!vSrc) {
        var s = n.querySelector("source");
        if (s) vSrc = s.getAttribute("src") || s.src || "";
      }
      var poster = n.getAttribute("poster") || "";
      if (vSrc) {
        return "\n\n![video" + (poster ? " poster=\"" + poster + "\"" : "") + "](" + vSrc + ")\n\n";
      }
    }

    if (tag === "iframe") {
      var ifrSrc = n.getAttribute("src") || n.src || "";
      if (ifrSrc && (ifrSrc.indexOf("muse.ai") >= 0 || ifrSrc.indexOf("youtube") >= 0 || ifrSrc.indexOf("vimeo") >= 0 || ifrSrc.indexOf("video") >= 0 || ifrSrc.indexOf("embed") >= 0)) {
        return "\n\n[embed-video](" + ifrSrc + ")\n\n";
      }
    }

    if (tag === "audio") {
      var aSrc = n.getAttribute("src") || n.currentSrc || n.src || "";
      if (!aSrc) {
        var as = n.querySelector("source");
        if (as) aSrc = as.getAttribute("src") || as.src || "";
      }
      if (aSrc) {
        return "\n\n[audio](" + aSrc + ")\n\n";
      }
    }

    // 3. Bảng dữ liệu (Table -> Markdown Table)
    if (tag === "table") {
      var rows = Array.from(n.querySelectorAll("tr"));
      if (rows.length === 0) return "";
      var mdRows = [];
      rows.forEach(function(tr, rIdx) {
        var cells = Array.from(tr.querySelectorAll("th, td"));
        if (cells.length === 0) return;
        var rowStr = "| " + cells.map(function(cell) {
          return Array.from(cell.childNodes).map(function(c){ return nodeToMd(c, listDepth); }).join("").trim().replace(/\|/g, "\\|").replace(/\n+/g, " ");
        }).join(" | ") + " |";
        mdRows.push(rowStr);
        if (rIdx === 0) {
          var sep = "| " + cells.map(function() { return "---"; }).join(" | ") + " |";
          mdRows.push(sep);
        }
      });
      return "\n\n" + mdRows.join("\n") + "\n\n";
    }

    // 4. Trích dẫn Blockquote
    if (tag === "blockquote") {
      var bTxt = Array.from(n.childNodes).map(function(c){ return nodeToMd(c, listDepth); }).join("").trim();
      return "\n\n" + bTxt.split("\n").map(function(l){ return "> " + l; }).join("\n") + "\n\n";
    }

    if (tag === "strong" || tag === "b") {
      return "**" + Array.from(n.childNodes).map(function(c){ return nodeToMd(c, listDepth); }).join("") + "**";
    }
    if (tag === "em" || tag === "i") {
      return "*" + Array.from(n.childNodes).map(function(c){ return nodeToMd(c, listDepth); }).join("") + "*";
    }

    if (tag === "a") {
      var txt = Array.from(n.childNodes).map(function(c){ return nodeToMd(c, listDepth); }).join("").trim();
      var href = n.getAttribute("href") || "";
      if (href.match(/\.(mp4|webm|mov|m4v)(\?.*)?$/i) || href.indexOf("muse.ai/v/") >= 0 || href.indexOf("muse.ai/embed/") >= 0) {
        return "\n\n![video](" + href + ")\n\n";
      }
      return href ? "[" + (txt || href) + "](" + href + ")" : txt;
    }

    if (tag === "ul" || tag === "ol") {
      return "\n" + Array.from(n.children).map(function(li, idx) {
        var prefix = tag === "ol" ? (idx + 1) + ". " : "- ";
        var indent = listDepth > 0 ? "  ".repeat(listDepth) : "";
        var body = Array.from(li.childNodes).map(function(c){ return nodeToMd(c, listDepth + 1); }).join("").trim();
        return indent + prefix + body;
      }).join("\n") + "\n\n";
    }
    if (tag === "li") {
      var ind = listDepth > 1 ? "  ".repeat(listDepth - 1) : "";
      var b = Array.from(n.childNodes).map(function(c){ return nodeToMd(c, listDepth); }).join("").trim();
      return ind + "- " + b + "\n";
    }

    if (tag === "p" || tag === "div" || tag === "h1" || tag === "h2" || tag === "h3" || tag === "h4" || tag === "h5" || tag === "h6") {
      var inner = Array.from(n.childNodes).map(function(c){ return nodeToMd(c, listDepth); }).join("").trim();
      return inner ? inner + "\n\n" : "";
    }
    if (tag === "br") return "\n";
    if (tag === "hr") return "\n\n---\n\n";
    if (tag === "img" && n.src) return "\n![" + (n.alt || "Hình ảnh") + "](" + n.src + ")\n";

    // 5. Bảo toàn các thẻ XML / Custom Element không bị nuốt mất thẻ tag
    if (!standardFormattingTags[tag] && tag.length > 0) {
      var attrs = Array.from(n.attributes || []).map(function(a){ return " " + a.name + "=\"" + a.value + "\""; }).join("");
      var innerXml = Array.from(n.childNodes).map(function(c){ return nodeToMd(c, listDepth); }).join("");
      return "<" + tag + attrs + ">" + innerXml + "</" + tag + ">";
    }

    return Array.from(n.childNodes).map(function(c){ return nodeToMd(c, listDepth); }).join("");
  }

  function findComposer() {
    var selectors = [
      'textarea[aria-label*="Nhắn tin" i]',
      'textarea[placeholder*="Nhắn tin" i]',
      'textarea[aria-label*="Message" i]',
      'textarea[placeholder*="Message" i]',
      'textarea[placeholder*="Prompt" i]',
      'textarea[placeholder*="Ask" i]',
      'textarea[data-testid*="composer" i]',
      'textarea[data-testid*="chat" i]',
      'div[data-testid*="composer" i]',
      'div[data-testid*="chat-input" i]',
      '[contenteditable="true"]',
      '[contenteditable="plaintext-only"]',
      '[role="textbox"]',
      '[data-lexical-editor="true"]',
      '[data-slate-editor="true"]',
      'textarea',
      'input[type="text"][placeholder*="Nhắn tin" i]',
      'input[type="text"][placeholder*="Message" i]',
      'input[type="text"][aria-label*="Message" i]'
    ];
    var all = Array.from(document.querySelectorAll(selectors.join(", ")));
    var valid = all.filter(function(item) {
      if (item.disabled) return false;
      var ph = (item.getAttribute("placeholder") || "").toLowerCase();
      var al = (item.getAttribute("aria-label") || "").toLowerCase();
      if (ph.includes("tìm kiếm") || ph.includes("search") || al.includes("search")) return false;
      var style = window.getComputedStyle(item);
      if (style.display === "none" || style.visibility === "hidden") return false;
      var rect = item.getBoundingClientRect();
      return (rect.width > 0 && rect.height > 0) || item.offsetParent !== null || style.opacity !== "0";
    });
    if (valid.length === 0) return null;
    valid.sort(function(a, b) {
      var aLabel = ((a.getAttribute("aria-label") || "") + " " + (a.getAttribute("placeholder") || "") + " " + (a.className || "")).toLowerCase();
      var bLabel = ((b.getAttribute("aria-label") || "") + " " + (b.getAttribute("placeholder") || "") + " " + (b.className || "")).toLowerCase();
      var aScore = (/message|chat|nhắn tin|tin nhắn|composer|prompt/i.test(aLabel) ? 100000 : 0) + (a.getBoundingClientRect().bottom || 0);
      var bScore = (/message|chat|nhắn tin|tin nhắn|composer|prompt/i.test(bLabel) ? 100000 : 0) + (b.getBoundingClientRect().bottom || 0);
      return bScore - aScore;
    });
    return valid[0];
  }

  var composer = findComposer();
  var compVal = composer ? ((composer.value !== undefined ? composer.value : composer.textContent) || "").trim() : "";
  var isGen = !!document.querySelector('.animate-spin, button[aria-label*="Stop" i], button[aria-label*="Dừng" i], button.stop-button, [data-is-generating="true"], .typing-indicator');
  // 1. Tìm các phần tử chứa câu trả lời của trợ lý (Assistant), loại trừ container bọc danh sách
  var assistantSelectors = [
    '[data-hatch-assistant-message-body="true"]',
    '[data-pel-click="assistant_message_content"]',
    '.prose',
    'div[data-message-author-role="assistant"]',
    'div[class*="message-assistant"]',
    '.chat-bubble-assistant',
    'div[data-testid*="assistant-message"]',
    'div[data-role="assistant"]'
  ];
  var rawEls = Array.from(document.querySelectorAll(assistantSelectors.join(", "))).filter(function(el) {
    if (el.closest('form, textarea, [contenteditable="true"]')) return false;
    if (el.closest('.bg-chat-user-bubble, div.items-end, [data-message-author-role="user"]')) return false;
    return true;
  });
  var leafEls = rawEls.filter(function(el) {
    return !rawEls.some(function(child) { return child !== el && el.contains(child); });
  });

  // 2. Tìm phần tử tin nhắn của User tương ứng với câu hỏi hiện tại
  var activePromptNorm = (window.__martActivePrompt || "").replace(/\s+/g, " ").trim().toLowerCase();
  var activePromptSnippet = activePromptNorm.length > 20 ? activePromptNorm.slice(0, 20) : activePromptNorm;
  var userTurnEl = null;
  if (activePromptSnippet) {
    var userCandidates = Array.from(document.querySelectorAll('.bg-chat-user-bubble, div.items-end .hatch-chat-groupable-bubble, [data-message-author-role="user"], div[class*="user"], .hatch-chat-groupable-bubble, div'));
    for (var u = userCandidates.length - 1; u >= 0; u--) {
      var uc = userCandidates[u];
      if (uc.closest('form, textarea, [contenteditable="true"]')) continue;
      var ut = (uc.innerText || uc.textContent || "").replace(/\s+/g, " ").trim().toLowerCase();
      if (ut && ut.indexOf(activePromptSnippet) >= 0) {
        userTurnEl = uc;
        break;
      }
    }
  }

  // 3. Trích xuất markdown cho toàn bộ assistant elements & khối nằm sau User
  var mdList = [];
  var postUserMdList = [];
  var targetEls = leafEls.length > 0 ? leafEls : rawEls;
  for (var i = 0; i < targetEls.length; i++) {
    var m = nodeToMd(targetEls[i], 0).replace(/\n{3,}/g, "\n\n").trim();
    if (!m) {
      m = (targetEls[i].innerText || targetEls[i].textContent || "").trim();
    }
    if (m.length > 0) {
      mdList.push(m);
      if (userTurnEl) {
        // Kiểm tra phần tử này có nằm SAU phần tử User trong thứ tự DOM không (Node.DOCUMENT_POSITION_FOLLOWING = 4)
        if (Boolean(userTurnEl.compareDocumentPosition(targetEls[i]) & 4)) {
          postUserMdList.push(m);
        }
      }
    }
  }

  var count = mdList.length;
  var lastMd = count > 0 ? mdList[count - 1] : "";
  var unseenCount = postUserMdList.length;
  var unseenLastMd = unseenCount > 0 ? postUserMdList[unseenCount - 1] : "";
  return JSON.stringify({
    hasComposer: !!composer,
    composerValue: compVal,
    isGenerating: isGen,
    userTurnSent: !!userTurnEl,
    count: count,
    lastMd: lastMd,
    unseenCount: unseenCount,
    unseenLastMd: unseenLastMd,
    url: location.href
  });
})()
`

type cdpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cdpRPCResponse struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *cdpRPCError    `json:"error"`
}

type cdpClient struct {
	ws       *websocket.Conn
	writeMu  sync.Mutex
	eventMu  sync.RWMutex
	onEvent  func(method string, params json.RawMessage)
	nextID   atomic.Int64
	pending  sync.Map
	closed   chan struct{}
	closeOne sync.Once
}

func (c *cdpClient) SetEventHandler(fn func(method string, params json.RawMessage)) {
	c.eventMu.Lock()
	c.onEvent = fn
	c.eventMu.Unlock()
}

func dialCDPClient(ctx context.Context, wsURL string) (*cdpClient, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	ws, _, err := websocket.Dial(dialCtx, wsURL, nil)
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(32 * 1024 * 1024)

	c := &cdpClient{
		ws:     ws,
		closed: make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

func (c *cdpClient) readLoop() {
	defer c.Close()
	for {
		_, data, err := c.ws.Read(context.Background())
		if err != nil {
			return
		}
		var msg cdpRPCResponse
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.ID > 0 {
			if chAny, ok := c.pending.LoadAndDelete(msg.ID); ok {
				if ch, okCast := chAny.(chan cdpRPCResponse); okCast {
					ch <- msg
				}
			}
		} else if msg.Method != "" {
			c.eventMu.RLock()
			fn := c.onEvent
			c.eventMu.RUnlock()
			if fn != nil {
				fn(msg.Method, msg.Params)
			}
		}
	}
}

func (c *cdpClient) Call(ctx context.Context, method string, params interface{}, out interface{}) error {
	select {
	case <-c.closed:
		return errors.New("ERR_CDP_CONNECTION_CLOSED")
	default:
	}

	id := c.nextID.Add(1)
	ch := make(chan cdpRPCResponse, 1)
	c.pending.Store(id, ch)
	defer c.pending.Delete(id)

	reqPayload := map[string]interface{}{
		"id":     id,
		"method": method,
	}
	if params != nil {
		reqPayload["params"] = params
	}
	raw, err := json.Marshal(reqPayload)
	if err != nil {
		return err
	}

	c.writeMu.Lock()
	errWrite := c.ws.Write(ctx, websocket.MessageText, raw)
	c.writeMu.Unlock()
	if errWrite != nil {
		return errWrite
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("CDP %s error (%d): %s", method, resp.Error.Code, resp.Error.Message)
		}
		if out != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, out)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return errors.New("ERR_CDP_CONNECTION_CLOSED")
	}
}

func (c *cdpClient) Close() {
	c.closeOne.Do(func() {
		close(c.closed)
		_ = c.ws.CloseNow()
	})
}

type RemoteLoginSession struct {
	mu               sync.RWMutex
	renderMu         sync.Mutex
	ID               string
	Label            string
	ProxyURL         string
	BrowserContextID string
	TargetID         string
	Page             *cdpClient
	Status           string // "waiting", "completed", "error", "cancelled"
	CurrentURL       string
	FrameDataURL     string
	FrameSeq         uint64
	Node             *MuseAccountNode
	ErrorMsg         string
	CreatedAt        time.Time
	LastActive       time.Time
	LastInteract     time.Time
	interceptCh      chan struct{}
	cancelFn         context.CancelFunc
}

type cdpWarmTab struct {
	Fingerprint      string
	BrowserContextID string
	TargetID         string
	Page             *cdpClient
	LastUsed         time.Time
}

type CDPBrowserManager struct {
	mu          sync.Mutex
	dataDir     string
	pool        *MuseRouterPool
	cmd         *exec.Cmd
	debugPort   int
	browserWS   *cdpClient
	sessionsMu  sync.RWMutex
	sessions    map[string]*RemoteLoginSession
	chatMu      sync.Mutex
	warmTabs    map[string]*cdpWarmTab
	lastUsed    time.Time
	stopReaper  chan struct{}
	reaperOnce  sync.Once
}

func NewCDPBrowserManager(dataDir string, pool *MuseRouterPool) *CDPBrowserManager {
	m := &CDPBrowserManager{
		dataDir:    dataDir,
		pool:       pool,
		sessions:   make(map[string]*RemoteLoginSession),
		warmTabs:   make(map[string]*cdpWarmTab),
		stopReaper: make(chan struct{}),
	}
	go m.idleReaperLoop()
	return m
}

func (m *CDPBrowserManager) HasBrowserAvailable() bool {
	bin, _ := findChromeBinary()
	return bin != ""
}

func findChromeBinary() (string, bool) {
	if envBin := strings.TrimSpace(os.Getenv("CHROME_BIN")); envBin != "" {
		if st, err := os.Stat(envBin); err == nil && !st.IsDir() {
			return envBin, strings.Contains(strings.ToLower(filepath.Base(envBin)), "headless-shell")
		}
	}

	exePath, _ := os.Executable()
	exeDir := filepath.Dir(exePath)

	var candidates []string
	if runtime.GOOS == "windows" {
		candidates = []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		}
	} else {
		candidates = []string{
			filepath.Join(".", "chrome_bin", "chrome-headless-shell-linux64", "chrome-headless-shell"),
			filepath.Join("..", "chrome_bin", "chrome-headless-shell-linux64", "chrome-headless-shell"),
			filepath.Join(exeDir, "chrome_bin", "chrome-headless-shell-linux64", "chrome-headless-shell"),
			"/usr/bin/google-chrome-stable",
			"/usr/bin/google-chrome",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
		}
	}

	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			abs, errAbs := filepath.Abs(c)
			if errAbs == nil {
				c = abs
			}
			return c, strings.Contains(strings.ToLower(filepath.Base(c)), "headless-shell")
		}
	}
	return "", false
}

func pickFreeLocalPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errors.New("ERR_NO_FREE_PORT")
	}
	return addr.Port, nil
}

func (m *CDPBrowserManager) ensureBrowserLocked(ctx context.Context) (*cdpClient, int, error) {
	if m.browserWS != nil && m.cmd != nil && m.cmd.Process != nil {
		select {
		case <-m.browserWS.closed:
			m.shutdownLocked()
		default:
			m.lastUsed = time.Now()
			return m.browserWS, m.debugPort, nil
		}
	}

	binPath, isHeadlessShell := findChromeBinary()
	if binPath == "" {
		return nil, 0, errors.New("Không tìm thấy trình duyệt Chrome/Chromium trên máy chủ")
	}

	port, err := pickFreeLocalPort()
	if err != nil {
		return nil, 0, err
	}

	profileDir := filepath.Join(m.dataDir, fmt.Sprintf("cdp_profile_%d", port))
	_ = os.MkdirAll(profileDir, 0700)

	headlessFlag := "--headless=new"
	if isHeadlessShell {
		headlessFlag = "--headless"
	}

	args := []string{
		headlessFlag,
		"--no-sandbox",
		"--disable-setuid-sandbox",
		"--disable-dev-shm-usage",
		"--disable-gpu",
		"--disable-software-rasterizer",
		"--disable-blink-features=AutomationControlled",
		"--user-agent=" + cdpDefaultUA,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-extensions",
		"--disable-component-update",
		"--disable-domain-reliability",
		"--disable-client-side-phishing-detection",
		"--metrics-recording-only",
		"--no-pings",
		"--disable-sync",
		"--disable-translate",
		"--mute-audio",
		"--renderer-process-limit=2",
		"--js-flags=--max-old-space-size=192",
		fmt.Sprintf("--window-size=%d,%d", cdpViewportWidth, cdpViewportHeight),
		"--remote-debugging-address=127.0.0.1",
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--remote-allow-origins=*",
		"--user-data-dir=" + profileDir,
		"about:blank",
	}

	cmd := exec.Command(binPath, args...)
	env := os.Environ()
	if runtime.GOOS != "windows" {
		exePath, _ := os.Executable()
		exeDir := filepath.Dir(exePath)
		var libDirs []string
		var shareDirs []string
		for _, base := range []string{".", "..", exeDir} {
			for _, sub := range []string{"usr/lib/x86_64-linux-gnu", "lib/x86_64-linux-gnu"} {
				cand := filepath.Join(base, "chrome_libs", sub)
				if st, errSt := os.Stat(cand); errSt == nil && st.IsDir() {
					if abs, errAbs := filepath.Abs(cand); errAbs == nil {
						libDirs = append(libDirs, abs)
					}
				}
			}
			candShare := filepath.Join(base, "chrome_libs", "usr", "share")
			if st, errSt := os.Stat(candShare); errSt == nil && st.IsDir() {
				if abs, errAbs := filepath.Abs(candShare); errAbs == nil {
					shareDirs = append(shareDirs, abs)
				}
			}
		}
		if len(libDirs) > 0 {
			curLD := os.Getenv("LD_LIBRARY_PATH")
			if curLD != "" {
				libDirs = append(libDirs, curLD)
			}
			env = append(env, "LD_LIBRARY_PATH="+strings.Join(libDirs, ":"))
		}
		if len(shareDirs) > 0 {
			curXDG := os.Getenv("XDG_DATA_DIRS")
			if curXDG == "" {
				curXDG = "/usr/local/share:/usr/share"
			}
			shareDirs = append(shareDirs, curXDG)
			env = append(env, "XDG_DATA_DIRS="+strings.Join(shareDirs, ":"))
		}
	}
	cmd.Env = env

	if err := cmd.Start(); err != nil {
		return nil, 0, fmt.Errorf("Không thể khởi động trình duyệt ngầm: %v", err)
	}

	// Chờ cổng CDP sẵn sàng
	versionURL := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
	httpCli := &http.Client{Timeout: 1500 * time.Millisecond}
	var wsDebuggerURL string
	deadline := time.Now().Add(14 * time.Second)
	for time.Now().Before(deadline) {
		resp, errGet := httpCli.Get(versionURL)
		if errGet == nil {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var parsed struct {
					WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
				}
				if json.Unmarshal(raw, &parsed) == nil && parsed.WebSocketDebuggerURL != "" {
					wsDebuggerURL = parsed.WebSocketDebuggerURL
					break
				}
			}
		}
		time.Sleep(120 * time.Millisecond)
	}

	if wsDebuggerURL == "" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = os.RemoveAll(profileDir)
		return nil, 0, errors.New("Trình duyệt ngầm khởi động quá thời gian chờ CDP")
	}

	bClient, err := dialCDPClient(ctx, wsDebuggerURL)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = os.RemoveAll(profileDir)
		return nil, 0, err
	}

	m.cmd = cmd
	m.debugPort = port
	m.browserWS = bClient
	m.lastUsed = time.Now()
	LogInfo("Started server-side headless browser on 127.0.0.1:%d (%s)", port, SanitizeLog(filepath.Base(binPath)))
	return bClient, port, nil
}

func (m *CDPBrowserManager) createIsolatedPage(ctx context.Context) (string, string, *cdpClient, error) {
	m.mu.Lock()
	bClient, port, err := m.ensureBrowserLocked(ctx)
	m.mu.Unlock()
	if err != nil {
		return "", "", nil, err
	}

	var ctxRes struct {
		BrowserContextID string `json:"browserContextId"`
	}
	if err := bClient.Call(ctx, "Target.createBrowserContext", map[string]interface{}{
		"disposeOnDetach": false,
	}, &ctxRes); err != nil {
		// Một số bản build không hỗ trợ đa context -> dùng context mặc định
		ctxRes.BrowserContextID = ""
	}

	createParams := map[string]interface{}{
		"url":    "about:blank",
		"width":  cdpViewportWidth,
		"height": cdpViewportHeight,
	}
	if ctxRes.BrowserContextID != "" {
		createParams["browserContextId"] = ctxRes.BrowserContextID
	}

	var targetRes struct {
		TargetID string `json:"targetId"`
	}
	if err := bClient.Call(ctx, "Target.createTarget", createParams, &targetRes); err != nil {
		return "", "", nil, err
	}

	pageWSURL := fmt.Sprintf("ws://127.0.0.1:%d/devtools/page/%s", port, targetRes.TargetID)
	pageClient, err := dialCDPClient(ctx, pageWSURL)
	if err != nil {
		_ = bClient.Call(context.Background(), "Target.closeTarget", map[string]interface{}{"targetId": targetRes.TargetID}, nil)
		return "", "", nil, err
	}

	_ = pageClient.Call(ctx, "Page.enable", nil, nil)
	_ = pageClient.Call(ctx, "Network.enable", nil, nil)
	_ = pageClient.Call(ctx, "Runtime.enable", nil, nil)
	_ = pageClient.Call(ctx, "Network.setUserAgentOverride", map[string]interface{}{
		"userAgent":      cdpDefaultUA,
		"acceptLanguage": "vi-VN,vi;q=0.9,en-US;q=0.8,en;q=0.7",
		"platform":       "Win32",
	}, nil)
	_ = pageClient.Call(ctx, "Emulation.setDeviceMetricsOverride", map[string]interface{}{
		"width":             cdpViewportWidth,
		"height":            cdpViewportHeight,
		"deviceScaleFactor": 1,
		"mobile":            false,
	}, nil)
	_ = pageClient.Call(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]interface{}{
		"source": cdpStealthAndPopupHookJS,
	}, nil)

	return ctxRes.BrowserContextID, targetRes.TargetID, pageClient, nil
}

func (m *CDPBrowserManager) closeIsolatedPage(browserCtxID string, targetID string, page *cdpClient) {
	if page != nil {
		page.Close()
	}
	m.mu.Lock()
	bClient := m.browserWS
	m.mu.Unlock()
	if bClient == nil {
		return
	}
	bg, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if targetID != "" {
		_ = bClient.Call(bg, "Target.closeTarget", map[string]interface{}{"targetId": targetID}, nil)
	}
	if browserCtxID != "" {
		_ = bClient.Call(bg, "Target.disposeBrowserContext", map[string]interface{}{"browserContextId": browserCtxID}, nil)
	}
}

func (m *CDPBrowserManager) StartRemoteLoginSession(parentCtx context.Context, label string, proxyURL string, startURL string) (*RemoteLoginSession, error) {
	ctx, cancel := context.WithTimeout(parentCtx, 25*time.Second)
	defer cancel()

	browserCtxID, targetID, page, err := m.createIsolatedPage(ctx)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(startURL) == "" {
		startURL = "https://muse.ai/api/oidc/start?return_to=%2F"
	}
	if err := page.Call(ctx, "Page.navigate", map[string]interface{}{"url": startURL}, nil); err != nil {
		m.closeIsolatedPage(browserCtxID, targetID, page)
		return nil, err
	}

	loopCtx, loopCancel := context.WithCancel(context.Background())
	interceptCh := make(chan struct{}, 1)
	page.SetEventHandler(func(method string, params json.RawMessage) {
		switch method {
		case "Network.responseReceivedExtraInfo", "Network.responseReceived", "Page.frameNavigated":
			raw := string(params)
			if strings.Contains(raw, "hatch_sess") || strings.Contains(raw, "/oidc/callback") || strings.Contains(raw, `"https://muse.ai/"`) {
				select {
				case interceptCh <- struct{}{}:
				default:
				}
			}
		}
	})

	sess := &RemoteLoginSession{
		ID:               fmt.Sprintf("rlogin_%d", time.Now().UnixNano()),
		Label:            strings.TrimSpace(label),
		ProxyURL:         strings.TrimSpace(proxyURL),
		BrowserContextID: browserCtxID,
		TargetID:         targetID,
		Page:             page,
		Status:           "waiting",
		CurrentURL:       startURL,
		CreatedAt:        time.Now(),
		LastActive:       time.Now(),
		LastInteract:     time.Now(),
		interceptCh:      interceptCh,
		cancelFn:         loopCancel,
	}

	m.sessionsMu.Lock()
	m.sessions[sess.ID] = sess
	m.sessionsMu.Unlock()

	go m.monitorRemoteLoginLoop(loopCtx, sess)
	return sess, nil
}

type cdpCookieEntry struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
}

func readMuseCookiesFromCDP(ctx context.Context, page *cdpClient, browserCtxID string) ([]cdpCookieEntry, string) {
	var res struct {
		Cookies []cdpCookieEntry `json:"cookies"`
	}
	params := map[string]interface{}{}
	if browserCtxID != "" {
		params["browserContextId"] = browserCtxID
	}
	if err := page.Call(ctx, "Storage.getCookies", params, &res); err != nil || len(res.Cookies) == 0 {
		_ = page.Call(ctx, "Network.getCookies", map[string]interface{}{
			"urls": []string{"https://muse.ai/", "https://www.muse.ai/", "https://auth.muse.ai/"},
		}, &res)
	}
	sessVal := ""
	var filtered []cdpCookieEntry
	seen := map[string]bool{}
	for _, c := range res.Cookies {
		dom := strings.TrimPrefix(strings.ToLower(c.Domain), ".")
		if strings.HasSuffix(dom, "muse.ai") && c.Name != "" && c.Value != "" {
			if c.Name == "hatch_sess" {
				sessVal = c.Value
			}
			if !seen[c.Name] {
				seen[c.Name] = true
				filtered = append(filtered, c)
			}
		}
	}
	return filtered, sessVal
}

func capturePageFrameAndURL(ctx context.Context, page *cdpClient) (string, string) {
	var urlEval struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	_ = page.Call(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    "location.href",
		"returnByValue": true,
	}, &urlEval)

	var shot struct {
		Data string `json:"data"`
	}
	errShot := page.Call(ctx, "Page.captureScreenshot", map[string]interface{}{
		"format":           "jpeg",
		"quality":          52,
		"optimizeForSpeed": true,
	}, &shot)
	if errShot != nil || shot.Data == "" {
		return "", urlEval.Result.Value
	}
	return "data:image/jpeg;base64," + shot.Data, urlEval.Result.Value
}

func (m *CDPBrowserManager) monitorRemoteLoginLoop(ctx context.Context, sess *RemoteLoginSession) {
	defer func() {
		m.closeIsolatedPage(sess.BrowserContextID, sess.TargetID, sess.Page)
	}()

	deadline := time.Now().Add(240 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		default:
		}

		sess.mu.RLock()
		lastAct := sess.LastActive
		curStatus := sess.Status
		sess.mu.RUnlock()
		if curStatus != "waiting" {
			return
		}
		if time.Since(lastAct) > 150*time.Second {
			sess.mu.Lock()
			sess.Status = "cancelled"
			sess.ErrorMsg = "Phiên đăng nhập tự đóng do không có thao tác"
			sess.mu.Unlock()
			return
		}

		stepCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		cookies, sessVal := readMuseCookiesFromCDP(stepCtx, sess.Page, sess.BrowserContextID)
		if sessVal != "" {
			// Chờ thêm 500ms để các cookie phụ (hatch_gw, datr) kịp ghi đầy đủ
			time.Sleep(500 * time.Millisecond)
			cookies, _ = readMuseCookiesFromCDP(stepCtx, sess.Page, sess.BrowserContextID)
			cancel()

			var parts []string
			for _, c := range cookies {
				parts = append(parts, c.Name+"="+c.Value)
			}
			cookieHdr := strings.Join(parts, "; ")

			saveCtx, saveCancel := context.WithTimeout(context.Background(), 20*time.Second)
			var savedNode *MuseAccountNode
			var saveErr error
			if sess.ProxyURL != "" {
				savedNode, saveErr = m.pool.AddAccountNode(saveCtx, sess.Label, "Muse.ai", "cookie", "https://muse.ai", cookieHdr, 0, sess.ProxyURL)
			} else {
				savedNode, _, saveErr = m.pool.IngestAccountCookie(saveCtx, sess.Label, cookieHdr)
			}
			saveCancel()

			sess.mu.Lock()
			if saveErr != nil {
				sess.Status = "error"
				sess.ErrorMsg = saveErr.Error()
			} else {
				sess.Status = "completed"
				sess.Node = savedNode
			}
			sess.mu.Unlock()
			return
		}

		sess.renderMu.Lock()
		frame, curURL := capturePageFrameAndURL(stepCtx, sess.Page)
		sess.renderMu.Unlock()
		cancel()

		sess.mu.Lock()
		if frame != "" && frame != sess.FrameDataURL {
			sess.FrameDataURL = frame
			sess.FrameSeq++
		}
		if curURL != "" {
			sess.CurrentURL = curURL
		}
		sinceInteract := time.Since(sess.LastInteract)
		sess.mu.Unlock()
		m.mu.Lock()
		m.lastUsed = time.Now()
		m.mu.Unlock()

		waitDur := 650 * time.Millisecond
		if sinceInteract > 5*time.Second {
			waitDur = 1800 * time.Millisecond
		}

		select {
		case <-ctx.Done():
			return
		case <-sess.interceptCh:
		case <-time.After(waitDur):
		}
	}

	sess.mu.Lock()
	if sess.Status == "waiting" {
		sess.Status = "error"
		sess.ErrorMsg = "Hết thời gian chờ đăng nhập (4 phút)"
	}
	sess.mu.Unlock()
}

func (m *CDPBrowserManager) GetRemoteLoginSnapshot(sessionID string, afterSeq ...uint64) (map[string]interface{}, error) {
	m.sessionsMu.RLock()
	sess, ok := m.sessions[sessionID]
	m.sessionsMu.RUnlock()
	if !ok {
		return nil, errors.New("ERR_SESSION_NOT_FOUND")
	}

	var clientSeq uint64
	if len(afterSeq) > 0 {
		clientSeq = afterSeq[0]
	}

	sess.mu.Lock()
	sess.LastActive = time.Now()
	out := map[string]interface{}{
		"session_id": sess.ID,
		"status":     sess.Status,
		"url":        sess.CurrentURL,
		"frame_seq":  sess.FrameSeq,
		"width":      cdpViewportWidth,
		"height":     cdpViewportHeight,
	}
	if clientSeq > 0 && sess.FrameSeq == clientSeq && sess.Status == "waiting" {
		out["unchanged"] = true
	} else {
		out["frame"] = sess.FrameDataURL
	}
	if sess.Node != nil {
		out["node"] = sess.Node
	}
	if sess.ErrorMsg != "" {
		out["error"] = sess.ErrorMsg
	}
	done := sess.Status != "waiting"
	sess.mu.Unlock()

	if done {
		go func(id string) {
			time.Sleep(15 * time.Second)
			m.sessionsMu.Lock()
			delete(m.sessions, id)
			m.sessionsMu.Unlock()
		}(sessionID)
	}
	return out, nil
}

type RemoteLoginInputReq struct {
	SessionID string  `json:"session_id"`
	Type      string  `json:"type"` // "click", "scroll", "text", "key", "clear", "reload"
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	DeltaX    float64 `json:"delta_x"`
	DeltaY    float64 `json:"delta_y"`
	Text      string  `json:"text"`
	Key       string  `json:"key"`
}

func virtualKeyCodeForKey(k string) (int, string) {
	switch k {
	case "Enter":
		return 13, "Enter"
	case "Backspace":
		return 8, "Backspace"
	case "Tab":
		return 9, "Tab"
	case "Escape":
		return 27, "Escape"
	case "Delete":
		return 46, "Delete"
	case "ArrowLeft":
		return 37, "ArrowLeft"
	case "ArrowUp":
		return 38, "ArrowUp"
	case "ArrowRight":
		return 39, "ArrowRight"
	case "ArrowDown":
		return 40, "ArrowDown"
	}
	return 0, ""
}

func (m *CDPBrowserManager) SendRemoteLoginInput(parentCtx context.Context, req RemoteLoginInputReq) (map[string]interface{}, error) {
	m.sessionsMu.RLock()
	sess, ok := m.sessions[req.SessionID]
	m.sessionsMu.RUnlock()
	if !ok {
		return nil, errors.New("ERR_SESSION_NOT_FOUND")
	}

	sess.mu.Lock()
	now := time.Now()
	sess.LastActive = now
	sess.LastInteract = now
	st := sess.Status
	page := sess.Page
	sess.mu.Unlock()

	if st != "waiting" || page == nil {
		return m.GetRemoteLoginSnapshot(req.SessionID)
	}

	ctx, cancel := context.WithTimeout(parentCtx, 5*time.Second)
	defer cancel()

	switch req.Type {
	case "click":
		_ = page.Call(ctx, "Input.dispatchMouseEvent", map[string]interface{}{
			"type": "mouseMoved",
			"x":    req.X,
			"y":    req.Y,
		}, nil)
		_ = page.Call(ctx, "Input.dispatchMouseEvent", map[string]interface{}{
			"type":       "mousePressed",
			"x":          req.X,
			"y":          req.Y,
			"button":     "left",
			"clickCount": 1,
		}, nil)
		_ = page.Call(ctx, "Input.dispatchMouseEvent", map[string]interface{}{
			"type":       "mouseReleased",
			"x":          req.X,
			"y":          req.Y,
			"button":     "left",
			"clickCount": 1,
		}, nil)
		time.Sleep(110 * time.Millisecond)

	case "scroll":
		_ = page.Call(ctx, "Input.dispatchMouseEvent", map[string]interface{}{
			"type":   "mouseWheel",
			"x":      req.X,
			"y":      req.Y,
			"deltaX": req.DeltaX,
			"deltaY": req.DeltaY,
		}, nil)
		time.Sleep(80 * time.Millisecond)

	case "text":
		if req.Text != "" {
			_ = page.Call(ctx, "Input.insertText", map[string]interface{}{
				"text": req.Text,
			}, nil)
			time.Sleep(80 * time.Millisecond)
		}

	case "key":
		vk, code := virtualKeyCodeForKey(req.Key)
		if vk > 0 {
			downParams := map[string]interface{}{
				"type":                  "rawKeyDown",
				"key":                   req.Key,
				"code":                  code,
				"windowsVirtualKeyCode": vk,
				"nativeVirtualKeyCode":  vk,
			}
			if req.Key == "Enter" {
				downParams["text"] = "\r"
				downParams["unmodifiedText"] = "\r"
			}
			_ = page.Call(ctx, "Input.dispatchKeyEvent", downParams, nil)
			if req.Key == "Enter" {
				_ = page.Call(ctx, "Input.dispatchKeyEvent", map[string]interface{}{
					"type": "char",
					"text": "\r",
				}, nil)
			}
			_ = page.Call(ctx, "Input.dispatchKeyEvent", map[string]interface{}{
				"type":                  "keyUp",
				"key":                   req.Key,
				"code":                  code,
				"windowsVirtualKeyCode": vk,
				"nativeVirtualKeyCode":  vk,
			}, nil)
			time.Sleep(90 * time.Millisecond)
		}

	case "clear":
		clearJS := `(function(){
			var el = document.activeElement;
			if (!el || (el.tagName !== 'INPUT' && el.tagName !== 'TEXTAREA')) return;
			var proto = el.tagName === 'TEXTAREA' ? window.HTMLTextAreaElement.prototype : window.HTMLInputElement.prototype;
			var desc = Object.getOwnPropertyDescriptor(proto, 'value');
			if (desc && desc.set) { desc.set.call(el, ''); } else { el.value = ''; }
			el.dispatchEvent(new Event('input', { bubbles: true }));
			el.dispatchEvent(new Event('change', { bubbles: true }));
		})()`
		_ = page.Call(ctx, "Runtime.evaluate", map[string]interface{}{"expression": clearJS}, nil)
		time.Sleep(60 * time.Millisecond)

	case "reload":
		_ = page.Call(ctx, "Page.reload", nil, nil)
		time.Sleep(250 * time.Millisecond)

	case "restart_login":
		_ = page.Call(ctx, "Page.navigate", map[string]interface{}{
			"url": "https://muse.ai/api/oidc/start?return_to=%2F",
		}, nil)
		time.Sleep(350 * time.Millisecond)
	}

	sess.renderMu.Lock()
	frame, curURL := capturePageFrameAndURL(ctx, page)
	sess.renderMu.Unlock()
	sess.mu.Lock()
	if frame != "" && frame != sess.FrameDataURL {
		sess.FrameDataURL = frame
		sess.FrameSeq++
	}
	if curURL != "" {
		sess.CurrentURL = curURL
	}
	sess.mu.Unlock()

	return m.GetRemoteLoginSnapshot(req.SessionID)
}

func (m *CDPBrowserManager) CancelRemoteLoginSession(sessionID string) {
	m.sessionsMu.Lock()
	sess, ok := m.sessions[sessionID]
	delete(m.sessions, sessionID)
	m.sessionsMu.Unlock()
	if !ok {
		return
	}
	sess.mu.Lock()
	if sess.Status == "waiting" {
		sess.Status = "cancelled"
	}
	cancel := sess.cancelFn
	sess.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

type cdpMuseSnap struct {
	HasComposer   bool   `json:"hasComposer"`
	ComposerValue string `json:"composerValue"`
	IsGenerating  bool   `json:"isGenerating"`
	UserTurnSent  bool   `json:"userTurnSent"`
	Count         int    `json:"count"`
	LastMd        string `json:"lastMd"`
	UnseenCount   int    `json:"unseenCount"`
	UnseenLastMd  string `json:"unseenLastMd"`
	URL           string `json:"url"`
}

func evalMuseSnap(ctx context.Context, page *cdpClient) (*cdpMuseSnap, error) {
	var evalRes struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err := page.Call(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    cdpMuseSnapshotJS,
		"returnByValue": true,
	}, &evalRes); err != nil {
		return nil, err
	}
	if evalRes.Result.Value == "" {
		return nil, errors.New("empty snapshot")
	}
	var snap cdpMuseSnap
	if err := json.Unmarshal([]byte(evalRes.Result.Value), &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func parseCookieHeaderToMap(raw string) map[string]string {
	out := map[string]string{}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return out
	}
	if !strings.Contains(trimmed, "=") {
		out["hatch_sess"] = trimmed
		return out
	}
	for _, part := range strings.Split(trimmed, ";") {
		p := strings.TrimSpace(part)
		idx := strings.IndexByte(p, '=')
		if idx > 0 {
			k := strings.TrimSpace(p[:idx])
			v := strings.TrimSpace(p[idx+1:])
			if k != "" && v != "" {
				out[k] = v
			}
		}
	}
	return out
}

func (m *CDPBrowserManager) getOrCreateWarmChatTab(ctx context.Context, fp string, cookieHeader string, targetOrigin string) (*cdpWarmTab, error) {
	if fp == "" {
		fp = "default_fp"
	}
	if targetOrigin == "" {
		targetOrigin = "https://muse.ai"
	}

	if existing, ok := m.warmTabs[fp]; ok && existing.Page != nil {
		select {
		case <-existing.Page.closed:
			delete(m.warmTabs, fp)
		default:
			if snap, err := evalMuseSnap(ctx, existing.Page); err == nil && snap.HasComposer {
				existing.LastUsed = time.Now()
				return existing, nil
			}
			m.closeIsolatedPage(existing.BrowserContextID, existing.TargetID, existing.Page)
			delete(m.warmTabs, fp)
		}
	}

	// Giới hạn tối đa 2 warm tabs để bảo vệ RAM 512MB trên Render
	if len(m.warmTabs) >= 2 {
		var oldestKey string
		var oldestTime time.Time
		for k, wt := range m.warmTabs {
			if oldestKey == "" || wt.LastUsed.Before(oldestTime) {
				oldestKey = k
				oldestTime = wt.LastUsed
			}
		}
		if oldestKey != "" {
			old := m.warmTabs[oldestKey]
			m.closeIsolatedPage(old.BrowserContextID, old.TargetID, old.Page)
			delete(m.warmTabs, oldestKey)
		}
	}

	browserCtxID, targetID, page, err := m.createIsolatedPage(ctx)
	if err != nil {
		return nil, err
	}

	cMap := parseCookieHeaderToMap(cookieHeader)
	if len(cMap) > 0 {
		var cdpCookies []map[string]interface{}
		isHTTPS := strings.HasPrefix(strings.ToLower(targetOrigin), "https://")
		for k, v := range cMap {
			entry := map[string]interface{}{
				"name":     k,
				"value":    v,
				"path":     "/",
				"secure":   isHTTPS,
				"httpOnly": k == "hatch_sess" || k == "datr",
			}
			if strings.Contains(targetOrigin, "muse.ai") {
				entry["domain"] = ".muse.ai"
			} else {
				entry["url"] = targetOrigin + "/"
			}
			cdpCookies = append(cdpCookies, entry)
		}
		_ = page.Call(ctx, "Network.setCookies", map[string]interface{}{"cookies": cdpCookies}, nil)
	}

	if err := page.Call(ctx, "Page.navigate", map[string]interface{}{"url": targetOrigin + "/"}, nil); err != nil {
		m.closeIsolatedPage(browserCtxID, targetID, page)
		return nil, err
	}

	wt := &cdpWarmTab{
		Fingerprint:      fp,
		BrowserContextID: browserCtxID,
		TargetID:         targetID,
		Page:             page,
		LastUsed:         time.Now(),
	}
	m.warmTabs[fp] = wt
	return wt, nil
}

func (m *CDPBrowserManager) ExecuteMuseChat(parentCtx context.Context, candidate *MuseAccountNode, userPrompt string, onChunk ...func(delta string, fullText string)) (string, int64, int, error) {
	rawCred, err := m.pool.rustBridge.DecryptSecret(candidate.EncryptedCredential)
	if err != nil {
		return "", 0, 0, err
	}

	m.chatMu.Lock()
	defer m.chatMu.Unlock()

	startTs := time.Now()
	ctx, cancel := context.WithTimeout(parentCtx, 90*time.Second)
	defer cancel()

	targetOrigin := strings.TrimRight(strings.TrimSpace(candidate.UpstreamURL), "/")
	if targetOrigin == "" {
		targetOrigin = "https://muse.ai"
	}

	wt, err := m.getOrCreateWarmChatTab(ctx, candidate.Fingerprint, rawCred, targetOrigin)
	if err != nil {
		return "", 0, 0, err
	}

	// Kiểm tra URL hiện tại của tab. Nếu tab chưa vào muse.ai thì điều hướng về targetOrigin
	var checkURLVal struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	_ = wt.Page.Call(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    "location.href",
		"returnByValue": true,
	}, &checkURLVal)

	currentTabURL := strings.ToLower(checkURLVal.Result.Value)
	cleanOrigin := strings.ToLower(targetOrigin)
	if currentTabURL == "" || (!strings.HasPrefix(currentTabURL, cleanOrigin) && !strings.Contains(currentTabURL, "muse.ai")) {
		_ = wt.Page.Call(ctx, "Page.navigate", map[string]interface{}{"url": targetOrigin + "/"}, nil)
		time.Sleep(1200 * time.Millisecond)
	}

	var baseline *cdpMuseSnap
	for i := 0; i < 50; i++ {
		snap, errSnap := evalMuseSnap(ctx, wt.Page)
		if errSnap == nil && snap.HasComposer {
			baseline = snap
			break
		}
		select {
		case <-ctx.Done():
			return "", 0, 0, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}

	if baseline == nil || !baseline.HasComposer {
		m.closeIsolatedPage(wt.BrowserContextID, wt.TargetID, wt.Page)
		delete(m.warmTabs, candidate.Fingerprint)
		return "", 0, 0, errors.New("Phiên đăng nhập muse.ai đã hết hạn hoặc trang chưa sẵn sàng — hãy đăng nhập lại tài khoản")
	}

	baselineLastMd := ""
	baselineCount := 0
	if baseline != nil {
		baselineLastMd = strings.TrimSpace(baseline.LastMd)
		baselineCount = baseline.Count
	}

	// 1. Điền nội dung vào ô nhập theo chuẩn React 16+ State Tracker & ContentEditable
	promptJSON, _ := json.Marshal(userPrompt)
	typeReactJS := fmt.Sprintf(`(function(txt){
		window.__martActivePrompt = txt;
		var selectors = [
			'textarea[aria-label*="Nhắn tin" i]',
			'textarea[placeholder*="Nhắn tin" i]',
			'textarea[aria-label*="Message" i]',
			'textarea[placeholder*="Message" i]',
			'textarea[placeholder*="Prompt" i]',
			'textarea[placeholder*="Ask" i]',
			'textarea[data-testid*="composer" i]',
			'textarea[data-testid*="chat" i]',
			'div[data-testid*="composer" i]',
			'div[data-testid*="chat-input" i]',
			'[contenteditable="true"]',
			'[contenteditable="plaintext-only"]',
			'[role="textbox"]',
			'[data-lexical-editor="true"]',
			'[data-slate-editor="true"]',
			'textarea',
			'input[type="text"][placeholder*="Nhắn tin" i]',
			'input[type="text"][placeholder*="Message" i]',
			'input[type="text"][aria-label*="Message" i]'
		];
		var all = Array.from(document.querySelectorAll(selectors.join(", ")));
		var valid = all.filter(function(item) {
			if (item.disabled) return false;
			var ph = (item.getAttribute("placeholder") || "").toLowerCase();
			var al = (item.getAttribute("aria-label") || "").toLowerCase();
			if (ph.includes("tìm kiếm") || ph.includes("search") || al.includes("search")) return false;
			var style = window.getComputedStyle(item);
			if (style.display === "none" || style.visibility === "hidden") return false;
			var rect = item.getBoundingClientRect();
			return (rect.width > 0 && rect.height > 0) || item.offsetParent !== null || style.opacity !== "0";
		});
		if (valid.length === 0) return {ok: false, err: "no composer"};
		valid.sort(function(a, b) {
			var aLabel = ((a.getAttribute("aria-label") || "") + " " + (a.getAttribute("placeholder") || "") + " " + (a.className || "")).toLowerCase();
			var bLabel = ((b.getAttribute("aria-label") || "") + " " + (b.getAttribute("placeholder") || "") + " " + (b.className || "")).toLowerCase();
			var aScore = (/message|chat|nhắn tin|tin nhắn|composer|prompt/i.test(aLabel) ? 100000 : 0) + (a.getBoundingClientRect().bottom || 0);
			var bScore = (/message|chat|nhắn tin|tin nhắn|composer|prompt/i.test(bLabel) ? 100000 : 0) + (b.getBoundingClientRect().bottom || 0);
			return bScore - aScore;
		});
		var ta = valid[0];
		ta.focus();
		if (ta.tagName === "TEXTAREA" || ta.tagName === "INPUT") {
			var proto = ta.tagName === "TEXTAREA" ? window.HTMLTextAreaElement.prototype : window.HTMLInputElement.prototype;
			var nativeSetter = Object.getOwnPropertyDescriptor(proto, 'value');
			if (nativeSetter && nativeSetter.set) {
				nativeSetter.set.call(ta, txt);
			} else {
				ta.value = txt;
			}
			if (ta._valueTracker) {
				try { ta._valueTracker.setValue(''); } catch(_) {}
			}
			ta.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText', data: txt }));
			ta.dispatchEvent(new Event('change', { bubbles: true }));
		} else {
			try {
				var sel = window.getSelection();
				var range = document.createRange();
				range.selectNodeContents(ta);
				sel.removeAllRanges();
				sel.addRange(range);
				document.execCommand("insertText", false, txt);
			} catch (_) {}
			if ((ta.textContent || "").trim() !== txt.trim()) {
				ta.textContent = txt;
			}
			ta.dispatchEvent(new InputEvent("input", { bubbles: true, data: txt, inputType: "insertText" }));
			ta.dispatchEvent(new Event("change", { bubbles: true }));
		}
		return {ok: true};
	})(%s)`, string(promptJSON))

	_ = wt.Page.Call(ctx, "Runtime.evaluate", map[string]interface{}{"expression": typeReactJS}, nil)
	time.Sleep(300 * time.Millisecond)

	// 2. Kích hoạt nút Gửi với cơ chế đa tầng (Button click + Form submit + CDP Enter key)
	clickSendOrEnter := func() {
		clickSendJS := `(function(){
			var selectors = [
				'button[data-pel-click="chat_send_message"]:not(:disabled)',
				'button[aria-label="Gửi"]:not(:disabled)',
				'button[aria-label*="Gửi" i]:not(:disabled)',
				'button[aria-label="Send"]:not(:disabled)',
				'button[aria-label*="Send" i]:not(:disabled)',
				'button[data-testid*="send" i]:not(:disabled)',
				'button[type="submit"]:not(:disabled)'
			];
			var btn = null;
			for (var s = 0; s < selectors.length; s++) {
				var b = document.querySelector(selectors[s]);
				if (b && !b.disabled) { btn = b; break; }
			}
			if (!btn) {
				var ta = document.querySelector('textarea, [contenteditable="true"]');
				var root = ta ? ta.parentElement : null;
				for (var d = 0; d < 6 && root; d++) {
					var b2 = root.querySelector('button[aria-label*="Gửi" i], button[aria-label*="Send" i], button[data-testid*="send" i], button[type="submit"], button:has(svg)');
					if (b2) { btn = b2; break; }
					root = root.parentElement;
				}
			}
			if (btn) {
				btn.disabled = false;
				btn.focus();
				var opts = { bubbles: true, cancelable: true, view: window };
				btn.dispatchEvent(new PointerEvent('pointerdown', opts));
				btn.dispatchEvent(new MouseEvent('mousedown', opts));
				btn.dispatchEvent(new PointerEvent('pointerup', opts));
				btn.dispatchEvent(new MouseEvent('mouseup', opts));
				btn.click();
				return true;
			}
			var activeTa = document.querySelector('textarea, [contenteditable="true"]');
			if (activeTa && activeTa.form) {
				try { activeTa.form.requestSubmit(); return true; } catch(_) {}
			}
			return false;
		})()`

		var clickRes struct {
			Result struct {
				Value bool `json:"value"`
			} `json:"result"`
		}
		_ = wt.Page.Call(ctx, "Runtime.evaluate", map[string]interface{}{
			"expression":    clickSendJS,
			"returnByValue": true,
		}, &clickRes)

		// Luôn gửi kèm native CDP Enter event để đảm bảo 100% trigger submit
		_ = wt.Page.Call(ctx, "Input.dispatchKeyEvent", map[string]interface{}{
			"type":                  "rawKeyDown",
			"key":                   "Enter",
			"code":                  "Enter",
			"windowsVirtualKeyCode": 13,
			"nativeVirtualKeyCode":  13,
			"text":                  "\r",
			"unmodifiedText":        "\r",
		}, nil)
		_ = wt.Page.Call(ctx, "Input.dispatchKeyEvent", map[string]interface{}{
			"type":                  "keyUp",
			"key":                   "Enter",
			"code":                  "Enter",
			"windowsVirtualKeyCode": 13,
			"nativeVirtualKeyCode":  13,
		}, nil)
	}

	clickSendOrEnter()

	cleanPrompt := strings.TrimSpace(userPrompt)
	deadline := time.Now().Add(60 * time.Second)
	lastNewMd := ""
	stableTicks := 0
	resendAttempts := 0

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", 0, 0, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}

		cur, errCur := evalMuseSnap(ctx, wt.Page)
		if errCur != nil || cur == nil {
			continue
		}

		// Nếu văn bản vẫn còn nguyên trong ô nhập sau 1.2s (chưa gửi đi được), gửi lại
		if cur.ComposerValue != "" && resendAttempts < 4 {
			resendAttempts++
			clickSendOrEnter()
			continue
		}

		replyMd := ""
		// 1. Ưu tiên hàng đầu: Nội dung assistant nằm SAU tin nhắn User vừa gửi (PostUserMd)
		if cur.UnseenLastMd != "" && cur.UnseenLastMd != cleanPrompt {
			replyMd = strings.TrimSpace(cur.UnseenLastMd)
		} else if cur.Count > baselineCount && cur.LastMd != "" && cur.LastMd != cleanPrompt {
			// 2. Số lượng khối assistant tăng lên so với ban đầu
			replyMd = strings.TrimSpace(cur.LastMd)
		} else if baselineLastMd != "" && cur.LastMd != "" && cur.LastMd != baselineLastMd && cur.LastMd != cleanPrompt {
			// 3. Khối assistant cuối cùng đã thay đổi nội dung (streaming / cập nhật)
			replyMd = strings.TrimSpace(cur.LastMd)
		} else if baselineLastMd == "" && cur.LastMd != "" && cur.LastMd != cleanPrompt {
			// 4. Ban đầu chưa có tin nào, nay xuất hiện tin mới
			replyMd = strings.TrimSpace(cur.LastMd)
		}

		if replyMd == "" {
			continue
		}

		// Tự động gia hạn deadline nếu Muse đang tiếp tục sinh nội dung (streaming)
		if cur.IsGenerating || replyMd != lastNewMd {
			if time.Until(deadline) < 25*time.Second {
				deadline = time.Now().Add(35 * time.Second)
			}
		}

		if replyMd == lastNewMd {
			stableTicks++
			// Nếu Muse không còn generating thì chỉ cần đứng yên 2 nhịp (~500ms) là hoàn thành ngay lập tức!
			// Nếu trang có cờ generating chưa kịp tắt thì đứng yên 5 nhịp (~1.25s) là hoàn thành.
			reqTicks := 2
			if cur.IsGenerating {
				reqTicks = 5
			}
			if stableTicks >= reqTicks {
				break
			}
		} else {
			delta := ""
			if strings.HasPrefix(replyMd, lastNewMd) {
				delta = replyMd[len(lastNewMd):]
			} else {
				delta = replyMd
			}
			lastNewMd = replyMd
			stableTicks = 0
			if len(onChunk) > 0 && onChunk[0] != nil && delta != "" {
				onChunk[0](delta, replyMd)
			}
		}
	}

	if lastNewMd == "" {
		return "", 0, 0, errors.New("Muse.ai trả về phản hồi rỗng; Mart AI chưa nhận được câu trả lời.")
	}

	wt.LastUsed = time.Now()
	m.mu.Lock()
	m.lastUsed = time.Now()
	m.mu.Unlock()

	latencyMs := int(time.Since(startTs).Milliseconds())
	estTokens := int64((len(cleanPrompt)+len(lastNewMd))/4) + 1
	return lastNewMd, estTokens, latencyMs, nil
}

func (m *CDPBrowserManager) idleReaperLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopReaper:
			return
		case <-ticker.C:
			m.sessionsMu.RLock()
			activeLoginCount := 0
			for _, s := range m.sessions {
				s.mu.RLock()
				if s.Status == "waiting" {
					activeLoginCount++
				}
				s.mu.RUnlock()
			}
			m.sessionsMu.RUnlock()
			if activeLoginCount > 0 {
				continue
			}

			m.mu.Lock()
			if m.cmd != nil && time.Since(m.lastUsed) > 180*time.Second {
				LogInfo("Reaping idle headless browser to free memory")
				m.shutdownLocked()
			}
			m.mu.Unlock()
		}
	}
}

func (m *CDPBrowserManager) shutdownLocked() {
	for k, wt := range m.warmTabs {
		if wt.Page != nil {
			wt.Page.Close()
		}
		delete(m.warmTabs, k)
	}
	if m.browserWS != nil {
		m.browserWS.Close()
		m.browserWS = nil
	}
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
		_ = m.cmd.Wait()
	}
	m.cmd = nil
	if m.debugPort > 0 {
		_ = os.RemoveAll(filepath.Join(m.dataDir, fmt.Sprintf("cdp_profile_%d", m.debugPort)))
		m.debugPort = 0
	}
}

func (m *CDPBrowserManager) Shutdown() {
	m.reaperOnce.Do(func() {
		close(m.stopReaper)
	})
	m.sessionsMu.Lock()
	for id, s := range m.sessions {
		if s.cancelFn != nil {
			s.cancelFn()
		}
		delete(m.sessions, id)
	}
	m.sessionsMu.Unlock()

	m.mu.Lock()
	m.shutdownLocked()
	m.mu.Unlock()
}

// HTTP Handlers cho Cửa Sổ Đăng Nhập Trực Tiếp Trên Web (0 Tiện ích, 0 F12)
func (a *ServerApp) handleRemoteLoginStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
		return
	}
	var req struct {
		Label    string `json:"label"`
		ProxyURL string `json:"proxy_url"`
	}
	_ = a.readJSONBody(r, &req)
	if a.pool.cdpManager == nil {
		WriteJSONError(w, http.StatusServiceUnavailable, "ERR_CDP_NOT_INITIALIZED")
		return
	}
	sess, err := a.pool.cdpManager.StartRemoteLoginSession(r.Context(), req.Label, req.ProxyURL, "")
	if err != nil {
		LogError("RemoteLoginStart error: %v", err)
		WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
		"session_id": sess.ID,
		"status":     "waiting",
		"width":      cdpViewportWidth,
		"height":     cdpViewportHeight,
	})
}

func (m *CDPBrowserManager) PrewarmAsync() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		m.mu.Lock()
		_, _, _ = m.ensureBrowserLocked(ctx)
		m.mu.Unlock()
	}()
}

func (a *ServerApp) handleRemoteLoginPrewarm(w http.ResponseWriter, r *http.Request) {
	if a.pool.cdpManager != nil && a.pool.cdpManager.HasBrowserAvailable() {
		a.pool.cdpManager.PrewarmAsync()
	}
	WriteJSONResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *ServerApp) handleRemoteLoginState(w http.ResponseWriter, r *http.Request) {
	sessID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessID == "" || a.pool.cdpManager == nil {
		WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_SESSION")
		return
	}
	var afterSeq uint64
	if rawSeq := strings.TrimSpace(r.URL.Query().Get("after_seq")); rawSeq != "" {
		if parsed, errP := strconv.ParseUint(rawSeq, 10, 64); errP == nil {
			afterSeq = parsed
		}
	}
	snap, err := a.pool.cdpManager.GetRemoteLoginSnapshot(sessID, afterSeq)
	if err != nil {
		WriteJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	WriteJSONResponse(w, http.StatusOK, snap)
}

func (a *ServerApp) handleRemoteLoginInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
		return
	}
	var req RemoteLoginInputReq
	if err := a.readJSONBody(r, &req); err != nil || strings.TrimSpace(req.SessionID) == "" {
		WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
		return
	}
	snap, err := a.pool.cdpManager.SendRemoteLoginInput(r.Context(), req)
	if err != nil {
		WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	WriteJSONResponse(w, http.StatusOK, snap)
}

func (a *ServerApp) handleRemoteLoginCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
		return
	}
	var req struct {
		SessionID string `json:"session_id"`
	}
	_ = a.readJSONBody(r, &req)
	if a.pool.cdpManager != nil && req.SessionID != "" {
		a.pool.cdpManager.CancelRemoteLoginSession(req.SessionID)
	}
	WriteJSONResponse(w, http.StatusOK, map[string]bool{"cancelled": true})
}

type CDPInspectReq struct {
	Action     string `json:"action"`     // "status", "navigate", "type_and_send", "eval", "click"
	Prompt     string `json:"prompt"`     // text for type_and_send
	URL        string `json:"url"`        // for navigate
	Expression string `json:"expression"` // for eval
	Selector   string `json:"selector"`   // for click
}

type CDPInspectResp struct {
	Success    bool                   `json:"success"`
	Error      string                 `json:"error,omitempty"`
	URL        string                 `json:"url"`
	Title      string                 `json:"title"`
	Composer   map[string]interface{} `json:"composer,omitempty"`
	Buttons    []interface{}          `json:"buttons,omitempty"`
	LastProse  string                 `json:"last_prose,omitempty"`
	ProseCount int                    `json:"prose_count"`
	IsGen      bool                   `json:"is_generating"`
	EvalResult interface{}            `json:"eval_result,omitempty"`
	Screenshot string                 `json:"screenshot,omitempty"`
}

func (m *CDPBrowserManager) InspectWarmTab(parentCtx context.Context, req CDPInspectReq) (*CDPInspectResp, error) {
	if m == nil || !m.HasBrowserAvailable() {
		return nil, errors.New("Trình duyệt CDP chưa khả dụng trên máy chủ")
	}

	nodes, err := m.pool.getOrderedCandidateNodes("")
	if err != nil || len(nodes) == 0 {
		return nil, errors.New("Chưa có tài khoản Muse nào khả dụng trong Pool")
	}
	var candidate *MuseAccountNode
	for _, n := range nodes {
		if n.AuthType == "cookie" && n.HasHatchSess {
			candidate = n
			break
		}
	}
	if candidate == nil {
		return nil, errors.New("Chưa có tài khoản Muse nào có cookie hatch_sess")
	}

	rawCred, err := m.pool.rustBridge.DecryptSecret(candidate.EncryptedCredential)
	if err != nil {
		return nil, fmt.Errorf("Lỗi giải mã thông tin tài khoản: %w", err)
	}

	ctx, cancel := context.WithTimeout(parentCtx, 45*time.Second)
	defer cancel()

	targetOrigin := strings.TrimRight(strings.TrimSpace(candidate.UpstreamURL), "/")
	if targetOrigin == "" {
		targetOrigin = "https://muse.ai"
	}

	wt, err := m.getOrCreateWarmChatTab(ctx, candidate.Fingerprint, rawCred, targetOrigin)
	if err != nil {
		return nil, err
	}

	resp := &CDPInspectResp{Success: true}

	if req.Action == "navigate" && req.URL != "" {
		_ = wt.Page.Call(ctx, "Page.navigate", map[string]interface{}{"url": req.URL}, nil)
		time.Sleep(3 * time.Second)
	} else if req.Action == "click" && req.Selector != "" {
		selJSON, _ := json.Marshal(req.Selector)
		clickJS := fmt.Sprintf(`(function(){ var el = document.querySelector(%s); if(el){ el.click(); return true; } return false; })()`, string(selJSON))
		var clickVal struct {
			Result struct {
				Value bool `json:"value"`
			} `json:"result"`
		}
		_ = wt.Page.Call(ctx, "Runtime.evaluate", map[string]interface{}{"expression": clickJS, "returnByValue": true}, &clickVal)
		time.Sleep(1 * time.Second)
	} else if req.Action == "eval" && req.Expression != "" {
		var evalVal struct {
			Result struct {
				Value interface{} `json:"value"`
			} `json:"result"`
		}
		_ = wt.Page.Call(ctx, "Runtime.evaluate", map[string]interface{}{"expression": req.Expression, "returnByValue": true}, &evalVal)
		resp.EvalResult = evalVal.Result.Value
	} else if req.Action == "type_and_send" && req.Prompt != "" {
		pJSON, _ := json.Marshal(req.Prompt)
		typeJS := fmt.Sprintf(`(function(txt){
			var el = document.querySelector('textarea, [contenteditable="true"], input[type="text"]');
			if (!el) return {ok: false, err: 'no composer'};
			el.focus();
			if (el.tagName === 'TEXTAREA' || el.tagName === 'INPUT') {
				var proto = el.tagName === 'TEXTAREA' ? window.HTMLTextAreaElement.prototype : window.HTMLInputElement.prototype;
				var desc = Object.getOwnPropertyDescriptor(proto, 'value');
				if (desc && desc.set) { desc.set.call(el, txt); } else { el.value = txt; }
				el.dispatchEvent(new InputEvent('input', { bubbles: true, data: txt, inputType: 'insertText' }));
				el.dispatchEvent(new Event('change', { bubbles: true }));
			}
			return {ok: true};
		})(%s)`, string(pJSON))
		var typeVal struct {
			Result struct {
				Value interface{} `json:"value"`
			} `json:"result"`
		}
		_ = wt.Page.Call(ctx, "Runtime.evaluate", map[string]interface{}{"expression": typeJS, "returnByValue": true}, &typeVal)
		time.Sleep(500 * time.Millisecond)

		sendJS := `(function(){
			var btns = Array.from(document.querySelectorAll('button'));
			return btns.map(function(b){
				return {
					tag: b.tagName,
					text: (b.innerText || '').trim(),
					aria: b.getAttribute('aria-label') || '',
					disabled: !!b.disabled,
					type: b.type,
					hasSvg: !!b.querySelector('svg')
				};
			});
		})()`
		var btnVal struct {
			Result struct {
				Value interface{} `json:"value"`
			} `json:"result"`
		}
		_ = wt.Page.Call(ctx, "Runtime.evaluate", map[string]interface{}{"expression": sendJS, "returnByValue": true}, &btnVal)
		resp.EvalResult = btnVal.Result.Value
	}

	diagJS := `(function(){
		var composer = document.querySelector('textarea, [contenteditable="true"]');
		var compInfo = null;
		if (composer) {
			var r = composer.getBoundingClientRect();
			compInfo = {
				tag: composer.tagName,
				placeholder: composer.getAttribute('placeholder') || '',
				ariaLabel: composer.getAttribute('aria-label') || '',
				value: (composer.value !== undefined ? composer.value : composer.textContent) || '',
				disabled: !!composer.disabled,
				width: r.width,
				height: r.height
			};
		}
		var btns = Array.from(document.querySelectorAll('button')).slice(0, 30).map(function(b){
			var br = b.getBoundingClientRect();
			return {
				text: (b.innerText || '').trim().slice(0, 50),
				aria: b.getAttribute('aria-label') || '',
				type: b.type,
				disabled: !!b.disabled,
				visible: br.width > 0 && br.height > 0,
				className: (b.className || '').slice(0, 100)
			};
		});
		var proses = Array.from(document.querySelectorAll('.prose, [data-message-author-role="assistant"]')).map(function(p){
			return (p.textContent || '').trim().slice(0, 200);
		});
		var isGen = !!document.querySelector('.animate-spin, button[aria-label*="Stop" i], button.stop-button, .typing-indicator');
		return {
			title: document.title,
			url: location.href,
			composer: compInfo,
			buttons: btns,
			proseCount: proses.length,
			lastProse: proses.length > 0 ? proses[proses.length - 1] : '',
			isGenerating: isGen
		};
	})()`

	var diagVal struct {
		Result struct {
			Value struct {
				Title       string                 `json:"title"`
				URL         string                 `json:"url"`
				Composer    map[string]interface{} `json:"composer"`
				Buttons     []interface{}          `json:"buttons"`
				ProseCount  int                    `json:"proseCount"`
				LastProse   string                 `json:"lastProse"`
				IsGen       bool                   `json:"isGenerating"`
			} `json:"result"`
		} `json:"result"`
	}
	_ = wt.Page.Call(ctx, "Runtime.evaluate", map[string]interface{}{"expression": diagJS, "returnByValue": true}, &diagVal)

	resp.Title = diagVal.Result.Value.Title
	resp.URL = diagVal.Result.Value.URL
	resp.Composer = diagVal.Result.Value.Composer
	resp.Buttons = diagVal.Result.Value.Buttons
	resp.ProseCount = diagVal.Result.Value.ProseCount
	resp.LastProse = diagVal.Result.Value.LastProse
	resp.IsGen = diagVal.Result.Value.IsGen

	frame, curURL := capturePageFrameAndURL(ctx, wt.Page)
	if frame != "" {
		resp.Screenshot = frame
	}
	if curURL != "" {
		resp.URL = curURL
	}

	return resp, nil
}

func (a *ServerApp) handleCDPInspect(w http.ResponseWriter, r *http.Request) {
	if a.pool.cdpManager == nil {
		WriteJSONError(w, http.StatusServiceUnavailable, "CDP_MANAGER_NOT_INITIALIZED")
		return
	}
	var req CDPInspectReq
	if r.Method == http.MethodPost {
		_ = a.readJSONBody(r, &req)
	}
	resp, err := a.pool.cdpManager.InspectWarmTab(r.Context(), req)
	if err != nil {
		WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	WriteJSONResponse(w, http.StatusOK, resp)
}

