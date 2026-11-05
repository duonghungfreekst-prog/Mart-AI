/* Mart AI Extension — Service Worker (v1.1.0):
   1) Giao tiếp 1 chạm với trang /admin của Mart AI để bắt thẳng hatch_sess tự động 100%
   2) Thực thi Chat thật trực tiếp với https://muse.ai khi máy chủ Mart AI chạy trên Cloud/Render (không có Python Playwright)
   3) Tự động đẩy về /api/accounts/ingest khi bật chế độ Tự động gửi trong Popup */
"use strict";

importScripts("common.js");

var pendingTimer = null;
var chatLockPromise = Promise.resolve();
var isApplyingRelayCookie = false;

var MART_TAB_PATTERNS = [
  "https://mart-ai.onrender.com/*",
  "https://*.onrender.com/*",
  "http://127.0.0.1/*",
  "http://localhost/*",
];

function setBadge(text, color) {
  chrome.action.setBadgeText({ text: text });
  chrome.action.setBadgeBackgroundColor({ color: color });
}

function sleepMs(ms) {
  return new Promise(function (resolve) {
    setTimeout(resolve, ms);
  });
}

async function broadcastSessionReadyToOpenTabs(injectIfMissing) {
  var cookies = await martCollectMuseCookies();
  if (cookies.sess) {
    setBadge("OK", "#16a34a");
  }
  chrome.tabs.query({ url: MART_TAB_PATTERNS }, function (tabs) {
    (tabs || []).forEach(function (t) {
      if (!t || !t.id) return;
      if (injectIfMissing) {
        try {
          chrome.scripting.executeScript({ target: { tabId: t.id }, files: ["content.js"] }, function () {
            void chrome.runtime.lastError;
            chrome.tabs.sendMessage(
              t.id,
              {
                __mart_bg_event: true,
                event: "MART_EXT_SESSION_READY",
                cookie: cookies.sess ? cookies.header : "",
                hasExistingSession: !!cookies.sess,
                version: "1.2.0",
              },
              function () {
                void chrome.runtime.lastError;
              }
            );
          });
        } catch (_) {}
      } else {
        chrome.tabs.sendMessage(
          t.id,
          {
            __mart_bg_event: true,
            event: "MART_EXT_SESSION_READY",
            cookie: cookies.sess ? cookies.header : "",
            hasExistingSession: !!cookies.sess,
            version: "1.2.0",
          },
          function () {
            void chrome.runtime.lastError;
          }
        );
      }
    });
  });
}

async function autoSendIfNeeded() {
  await broadcastSessionReadyToOpenTabs(false);
  var cfg = await martLoadConfig();
  if (!cfg.auto || !cfg.password) return;

  var cookies = await martCollectMuseCookies();
  if (!cookies.sess || cookies.sess === cfg.lastSess) return;

  var res = await martSendAccount(cfg);
  if (res.ok) {
    await martSaveConfig({ lastSess: res.sess });
    setBadge("OK", "#16a34a");
  } else {
    setBadge("!", "#dc2626");
  }
}

chrome.runtime.onInstalled.addListener(function () {
  broadcastSessionReadyToOpenTabs(true);
});

// Khi Service Worker thức dậy, tự tiêm cầu nối vào các tab Mart AI đang mở sẵn
broadcastSessionReadyToOpenTabs(true);

chrome.cookies.onChanged.addListener(function (change) {
  if (isApplyingRelayCookie) return;
  var c = change.cookie;
  if (!c || change.removed) return;
  if (c.name !== "hatch_sess" || !/(^|\.)muse\.ai$/.test(c.domain.replace(/^\./, ""))) return;
  if (pendingTimer) clearTimeout(pendingTimer);
  pendingTimer = setTimeout(function () {
    pendingTimer = null;
    autoSendIfNeeded();
  }, 1500);
});

// Mở tab đăng nhập chính thức của muse.ai, chờ hatch_sess xuất hiện và trả thẳng về trang Mart AI
async function interactiveCaptureFlow(options) {
  var opts = options || {};
  var existing = await martCollectMuseCookies();

  if (opts.onlyExisting) {
    if (existing.sess) {
      setBadge("OK", "#16a34a");
      return { ok: true, cookie: existing.header, immediate: true };
    }
    return { ok: false, error: "Chưa có phiên muse.ai nào đang đăng nhập trên trình duyệt này." };
  }

  var previousSess = existing.sess || "";
  if (opts.clearExistingSession) {
    await martClearMuseSessionCookies();
    previousSess = "";
  }

  var loginUrl = opts.loginUrl || "https://muse.ai/api/oidc/start?return_to=%2F";
  var openedTab = await chrome.tabs.create({ url: loginUrl, active: true });
  var openedTabId = openedTab ? openedTab.id : null;

  return new Promise(function (resolve) {
    var finished = false;
    var deadline = Date.now() + 180 * 1000;

    function cleanup() {
      finished = true;
      clearInterval(poller);
      chrome.tabs.onRemoved.removeListener(onTabRemoved);
    }

    function onTabRemoved(tabId) {
      if (tabId === openedTabId && !finished) {
        martCollectMuseCookies().then(function (cur) {
          if (!finished) {
            cleanup();
            if (cur.sess && (!opts.requireNewSess || cur.sess !== previousSess)) {
              setBadge("OK", "#16a34a");
              resolve({ ok: true, cookie: cur.header });
            } else {
              resolve({ ok: false, error: "Đã đóng cửa sổ đăng nhập trước khi hoàn tất." });
            }
          }
        });
      }
    }

    chrome.tabs.onRemoved.addListener(onTabRemoved);

    var poller = setInterval(async function () {
      if (finished) return;
      if (Date.now() > deadline) {
        cleanup();
        resolve({ ok: false, error: "Hết thời gian chờ đăng nhập (3 phút)." });
        return;
      }

      var cur = await martCollectMuseCookies();
      if (!cur.sess) return;
      if (opts.requireNewSess && previousSess && cur.sess === previousSess) return;

      var landedOnMuse = true;
      if (openedTabId !== null) {
        try {
          var t = await chrome.tabs.get(openedTabId);
          var u = (t && t.url) || "";
          if (u && (/auth\.meta\.com/i.test(u) || /\/oidc\/(start|callback|error)/i.test(u))) {
            landedOnMuse = false;
          }
        } catch (_) {
          landedOnMuse = true;
        }
      }
      if (!landedOnMuse) return;

      cleanup();
      setBadge("OK", "#16a34a");
      if (openedTabId !== null) {
        try {
          await chrome.tabs.remove(openedTabId);
        } catch (_) {}
      }
      resolve({ ok: true, cookie: cur.header });
    }, 1200);
  });
}

// ============================================================================
// CLOUD BROWSER RELAY: THỰC THI CHAT THẬT TRỰC TIẾP TRÊN TAB MUSE.AI
// ============================================================================

function parseCookieHeaderMap(rawHeader) {
  var map = {};
  (rawHeader || "").split(";").forEach(function (part) {
    var idx = part.indexOf("=");
    if (idx > 0) {
      var k = part.slice(0, idx).trim();
      var v = part.slice(idx + 1).trim();
      if (k && v) map[k] = v;
    }
  });
  return map;
}

async function applyRelayAccountCookies(cookieHeader) {
  var desired = parseCookieHeaderMap(cookieHeader);
  var desiredSess = desired["hatch_sess"] || "";
  if (!desiredSess) return false;

  var current = await martCollectMuseCookies();
  if (current.sess === desiredSess) {
    return false;
  }

  var names = Object.keys(desired);
  isApplyingRelayCookie = true;
  try {
    for (var i = 0; i < names.length; i++) {
      var name = names[i];
      var val = desired[name];
      await new Promise(function (resolve) {
        chrome.cookies.set(
          {
            url: "https://muse.ai/",
            domain: ".muse.ai",
            path: "/",
            name: name,
            value: val,
            secure: true,
            httpOnly: name === "hatch_sess" || name === "datr",
            sameSite: "lax",
          },
          function () {
            resolve();
          }
        );
      });
    }
  } finally {
    setTimeout(function () {
      isApplyingRelayCookie = false;
    }, 300);
  }
  return true;
}

function waitForTabComplete(tabId, timeoutMs) {
  return new Promise(function (resolve) {
    var done = false;
    var timer = setTimeout(function () {
      if (!done) {
        done = true;
        chrome.tabs.onUpdated.removeListener(listener);
        resolve();
      }
    }, timeoutMs || 18000);

    function listener(updatedId, info) {
      if (updatedId === tabId && info.status === "complete" && !done) {
        done = true;
        clearTimeout(timer);
        chrome.tabs.onUpdated.removeListener(listener);
        resolve();
      }
    }
    chrome.tabs.onUpdated.addListener(listener);
  });
}

async function ensureMuseWorkerTab(switchedAccount) {
  var tabs = await chrome.tabs.query({ url: "https://muse.ai/*" });
  var candidateTab = null;
  for (var i = 0; i < tabs.length; i++) {
    var u = tabs[i].url || "";
    if (!/\/oidc\//i.test(u)) {
      candidateTab = tabs[i];
      break;
    }
  }

  if (candidateTab) {
    if (switchedAccount || candidateTab.status !== "complete") {
      await chrome.tabs.reload(candidateTab.id);
      await waitForTabComplete(candidateTab.id, 18000);
    }
    return candidateTab.id;
  }

  var created = await chrome.tabs.create({
    url: "https://muse.ai/",
    active: false,
    pinned: true,
  });
  await waitForTabComplete(created.id, 20000);
  return created.id;
}

// Hàm chạy bên trong ngữ cảnh trang https://muse.ai: đọc trạng thái .prose và kiểm tra ô chat
function domReadMuseSnapshot() {
  var standardFormattingTags = {
    "p":1,"div":1,"span":1,"h1":1,"h2":1,"h3":1,"h4":1,"h5":1,"h6":1,
    "strong":1,"b":1,"em":1,"i":1,"u":1,"s":1,"a":1,"ul":1,"ol":1,"li":1,
    "table":1,"thead":1,"tbody":1,"tfoot":1,"tr":1,"th":1,"td":1,"blockquote":1,
    "pre":1,"code":1,"img":1,"video":1,"source":1,"audio":1,"iframe":1,
    "br":1,"hr":1,"button":1,"svg":1,"style":1,"script":1,"input":1,"textarea":1
  };

  function nodeToMd(n, listDepth) {
    if (n.nodeType === Node.TEXT_NODE) return n.textContent || "";
    if (n.nodeType !== Node.ELEMENT_NODE) return "";
    var tag = n.tagName.toLowerCase();
    if (tag === "button" || tag === "svg" || tag === "style" || tag === "script") return "";

    // 1. Code Block: Giữ nguyên thụt dòng, tên ngôn ngữ hoặc tự nhận diện XML/HTML/JSON
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
      return "\n```" + lang + "\n" + rawCode.replace(/\n$/, "") + "\n```\n\n";
    }

    if (tag === "code" && (!n.parentElement || n.parentElement.tagName.toLowerCase() !== "pre")) {
      return "`" + (n.textContent || "") + "`";
    }

    // 2. Video Player & Media
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

    // 3. Table -> Markdown Table
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

    // 4. Blockquote
    if (tag === "blockquote") {
      var bTxt = Array.from(n.childNodes).map(function(c){ return nodeToMd(c, listDepth); }).join("").trim();
      return "\n\n" + bTxt.split("\n").map(function(l){ return "> " + l; }).join("\n") + "\n\n";
    }

    if (tag === "strong" || tag === "b") {
      return "**" + Array.from(n.childNodes).map(function (c) { return nodeToMd(c, listDepth); }).join("") + "**";
    }
    if (tag === "em" || tag === "i") {
      return "*" + Array.from(n.childNodes).map(function (c) { return nodeToMd(c, listDepth); }).join("") + "*";
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
      return "\n" + Array.from(n.children).map(function (c) { return nodeToMd(c, listDepth + 1); }).join("");
    }
    if (tag === "li") {
      var indent = listDepth > 1 ? "  ".repeat(listDepth - 1) : "";
      var body = Array.from(n.childNodes).map(function (c) { return nodeToMd(c, listDepth); }).join("").trim();
      return indent + "- " + body + "\n";
    }
    if (tag === "p" || tag === "div" || tag === "h1" || tag === "h2" || tag === "h3" || tag === "h4" || tag === "h5" || tag === "h6") {
      var inner = Array.from(n.childNodes).map(function (c) { return nodeToMd(c, listDepth); }).join("").trim();
      return inner ? inner + "\n\n" : "";
    }
    if (tag === "br") return "\n";
    if (tag === "hr") return "\n\n---\n\n";
    if (tag === "img" && n.src) {
      return "\n![" + (n.alt || "Hình ảnh") + "](" + n.src + ")\n";
    }

    // 5. Bảo toàn các thẻ XML / Custom Element
    if (!standardFormattingTags[tag] && tag.length > 0) {
      var attrs = Array.from(n.attributes || []).map(function(a){ return " " + a.name + "=\"" + a.value + "\""; }).join("");
      var innerXml = Array.from(n.childNodes).map(function(c){ return nodeToMd(c, listDepth); }).join("");
      return "<" + tag + attrs + ">" + innerXml + "</" + tag + ">";
    }

    return Array.from(n.childNodes).map(function (c) { return nodeToMd(c, listDepth); }).join("");
  }

  var composer = document.querySelector('textarea[aria-label="Message"], textarea, [contenteditable="true"]');
  var isGen = !!document.querySelector('.animate-spin, button[aria-label*="Stop" i], button[aria-label*="Dừng" i], button.stop-button, [data-is-generating="true"], .typing-indicator, .pulse');
  var els = Array.from(document.querySelectorAll('.prose, [data-message-author-role="assistant"], div[class*="prose"], div[class*="message-assistant"], .chat-bubble-assistant'));
  var count = els.length;
  var lastMd = count > 0 ? nodeToMd(els[count - 1], 0).replace(/\n{3,}/g, "\n\n").trim() : "";
  return {
    hasComposer: !!composer,
    isGenerating: isGen,
    count: count,
    lastMd: lastMd,
    url: location.href,
  };
}

// Hàm chạy bên trong trang https://muse.ai: điền prompt vào composer và nhấn gửi
function domSubmitMusePrompt(promptText) {
  var el = document.querySelector('textarea[aria-label="Message"], textarea, [contenteditable="true"]');
  if (!el) return { ok: false, error: "Không tìm thấy ô nhập tin nhắn trên muse.ai" };

  el.focus();
  if (el.tagName === "TEXTAREA" || el.tagName === "INPUT") {
    var proto = el.tagName === "TEXTAREA" ? window.HTMLTextAreaElement.prototype : window.HTMLInputElement.prototype;
    var desc = Object.getOwnPropertyDescriptor(proto, "value");
    if (desc && desc.set) {
      desc.set.call(el, promptText);
    } else {
      el.value = promptText;
    }
    el.dispatchEvent(new InputEvent("input", { bubbles: true, data: promptText, inputType: "insertText" }));
    el.dispatchEvent(new Event("change", { bubbles: true }));
  } else {
    document.execCommand("selectAll", false, null);
    document.execCommand("insertText", false, promptText);
    el.dispatchEvent(new InputEvent("input", { bubbles: true, data: promptText, inputType: "insertText" }));
  }

  return new Promise(function (resolve) {
    setTimeout(function () {
      var clicked = false;
      if (el.form) {
        var submitBtn = el.form.querySelector('button[type="submit"]:not(:disabled)');
        if (submitBtn) {
          submitBtn.click();
          clicked = true;
        } else if (typeof el.form.requestSubmit === "function") {
          try {
            el.form.requestSubmit();
            clicked = true;
          } catch (_) {}
        }
      }
      if (!clicked) {
        var root = el.parentElement;
        for (var d = 0; d < 4 && root; d++) {
          var sendBtn = root.querySelector(
            'button[type="submit"]:not(:disabled), button[aria-label*="Send" i]:not(:disabled), button[aria-label*="Gửi" i]:not(:disabled)'
          );
          if (sendBtn) {
            sendBtn.click();
            clicked = true;
            break;
          }
          root = root.parentElement;
        }
      }
      function pressEnterOnEl() {
        var evOpts = { key: "Enter", code: "Enter", keyCode: 13, which: 13, bubbles: true, cancelable: true };
        el.dispatchEvent(new KeyboardEvent("keydown", evOpts));
        el.dispatchEvent(new KeyboardEvent("keypress", evOpts));
        el.dispatchEvent(new KeyboardEvent("keyup", evOpts));
      }
      if (!clicked) {
        pressEnterOnEl();
        resolve({ ok: true });
      } else {
        setTimeout(function () {
          var remaining = (el.value !== undefined ? el.value : el.textContent || "").trim();
          if (remaining.length > 0) {
            pressEnterOnEl();
          }
          resolve({ ok: true });
        }, 120);
      }
    }, 140);
  });
}

async function evalInTab(tabId, func, args) {
  var results = await chrome.scripting.executeScript({
    target: { tabId: tabId },
    func: func,
    args: args || [],
  });
  return results && results[0] ? results[0].result : null;
}

async function executeMuseChatDirect(prompt, cookieHeader) {
  var startTs = Date.now();
  var switched = await applyRelayAccountCookies(cookieHeader || "");
  var tabId = await ensureMuseWorkerTab(switched);

  // Chờ composer sẵn sàng & lịch sử .prose ổn định
  var baseline = null;
  var prevCnt = -1;
  for (var w = 0; w < 35; w++) {
    var snap = await evalInTab(tabId, domReadMuseSnapshot, []);
    if (snap && snap.hasComposer) {
      if (w >= 2 && snap.count === prevCnt) {
        baseline = snap;
        break;
      }
      prevCnt = snap.count;
      baseline = snap;
    }
    await sleepMs(300);
  }

  if (!baseline || !baseline.hasComposer) {
    return {
      ok: false,
      error: "Phiên đăng nhập muse.ai đã hết hạn hoặc trang chưa sẵn sàng — hãy bấm Đồng bộ lại tài khoản.",
    };
  }

  var sendRes = await evalInTab(tabId, domSubmitMusePrompt, [prompt]);
  if (!sendRes || !sendRes.ok) {
    return { ok: false, error: (sendRes && sendRes.error) || "Không gửi được tin nhắn vào muse.ai." };
  }

  var deadline = Date.now() + 75000;
  var lastNewMd = "";
  var stableTicks = 0;
  var cleanPrompt = (prompt || "").trim();

  while (Date.now() < deadline) {
    await sleepMs(260);
    var cur = await evalInTab(tabId, domReadMuseSnapshot, []);
    if (!cur) continue;

    var isNew =
      (cur.count > baseline.count && cur.lastMd.length > 0) ||
      (cur.count > 0 && cur.lastMd.length > 0 && cur.lastMd !== baseline.lastMd);

    if (!isNew || cur.lastMd === cleanPrompt) {
      continue;
    }

    // Nếu Muse đang sinh câu trả lời hoặc render video/media (isGenerating = true), tiếp tục chờ
    if (cur.isGenerating) {
      lastNewMd = cur.lastMd;
      stableTicks = 0;
      continue;
    }

    if (cur.lastMd === lastNewMd) {
      stableTicks += 1;
      // Chờ ít nhất 5 nhịp (~1.3s) ổn định sau khi Muse đã ngừng generating để bắt trọn vẹn media
      if (stableTicks >= 5) {
        break;
      }
    } else {
      lastNewMd = cur.lastMd;
      stableTicks = 0;
    }
  }

  if (!lastNewMd) {
    return { ok: false, error: "Đã gửi câu hỏi tới muse.ai nhưng chưa nhận được câu trả lời mới." };
  }

  var latencyMs = Date.now() - startTs;
  var estTokens = Math.max(1, Math.floor((cleanPrompt.length + lastNewMd.length) / 4));
  setBadge("OK", "#16a34a");
  return {
    ok: true,
    content: lastNewMd,
    tokens_used: estTokens,
    latency_ms: latencyMs,
  };
}

function enqueueMuseChat(prompt, cookieHeader) {
  var next = chatLockPromise.then(
    function () {
      return executeMuseChatDirect(prompt, cookieHeader);
    },
    function () {
      return executeMuseChatDirect(prompt, cookieHeader);
    }
  );
  chatLockPromise = next.catch(function () {});
  return next;
}

function handleBridgeAction(msg, sendResponse) {
  var action = (msg && msg.action) || "";
  if (action === "ping") {
    martCollectMuseCookies().then(function (c) {
      sendResponse({ ok: true, version: "1.2.0", hasExistingSession: !!c.sess, supportsChatRelay: true });
    });
    return true;
  }
  if (action === "ping_with_cookie") {
    martCollectMuseCookies().then(function (c) {
      sendResponse({
        ok: true,
        version: "1.2.0",
        hasExistingSession: !!c.sess,
        supportsChatRelay: true,
        cookie: c.sess ? c.header : "",
      });
    });
    return true;
  }
  if (action === "capture_existing") {
    interactiveCaptureFlow({ onlyExisting: true }).then(sendResponse);
    return true;
  }
  if (action === "login_and_capture") {
    interactiveCaptureFlow({
      loginUrl: msg.loginUrl || "https://muse.ai/api/oidc/start?return_to=%2F",
      clearExistingSession: !!msg.clearExistingSession,
      requireNewSess: !!msg.requireNewSess,
    }).then(sendResponse);
    return true;
  }
  if (action === "execute_muse_chat") {
    enqueueMuseChat(msg.prompt || "", msg.cookie || "")
      .then(sendResponse)
      .catch(function (err) {
        sendResponse({ ok: false, error: (err && err.message) || "Lỗi thực thi trên tab muse.ai" });
      });
    return true;
  }
  if (action === "broadcast_now") {
    broadcastSessionReadyToOpenTabs(true).then(function () {
      sendResponse({ ok: true });
    });
    return true;
  }
  return false;
}

chrome.runtime.onMessage.addListener(function (msg, _sender, sendResponse) {
  return handleBridgeAction(msg, sendResponse);
});

chrome.runtime.onMessageExternal.addListener(function (msg, _sender, sendResponse) {
  if (handleBridgeAction(msg, sendResponse)) {
    return true;
  }
  sendResponse({ ok: false, error: "UNKNOWN_ACTION" });
  return false;
});
