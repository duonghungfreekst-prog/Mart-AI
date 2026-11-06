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

function withTimeout(promise, timeoutMs, message) {
  return new Promise(function (resolve, reject) {
    var timer = setTimeout(function () { reject(new Error(message)); }, timeoutMs);
    Promise.resolve(promise).then(
      function (value) { clearTimeout(timer); resolve(value); },
      function (error) { clearTimeout(timer); reject(error); }
    );
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
                version: "1.2.8",
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
            version: "1.2.8",
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
  var trimmed = (rawHeader || "").trim();
  if (!trimmed) return map;
  if (trimmed.indexOf("=") === -1 && trimmed.indexOf(";") === -1 && trimmed.length >= 12) {
    map["hatch_sess"] = trimmed;
    return map;
  }
  trimmed.split(";").forEach(function (part) {
    var idx = part.indexOf("=");
    if (idx > 0) {
      var k = part.slice(0, idx).trim();
      var v = part.slice(idx + 1).trim();
      if (k && v) map[k] = v;
    }
  });
  return map;
}

function safeDecodeCookieVal(val) {
  var s = (val || "").trim();
  if (s.indexOf("%") >= 0) {
    try {
      return decodeURIComponent(s);
    } catch (_) {
      return s;
    }
  }
  return s;
}

async function applyRelayAccountCookies(cookieHeader) {
  var desired = parseCookieHeaderMap(cookieHeader);
  var desiredSess = safeDecodeCookieVal(desired["hatch_sess"] || "");
  if (!desiredSess) return false;

  var current = await martCollectMuseCookies();
  if (safeDecodeCookieVal(current.sess || "") === desiredSess) {
    return false;
  }

  var names = Object.keys(desired);
  isApplyingRelayCookie = true;
  try {
    await martClearMuseSessionCookies();
    for (var i = 0; i < names.length; i++) {
      var name = names[i];
      var val = safeDecodeCookieVal(desired[name]);
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

var martCachedWorkerTabId = null;

chrome.tabs.onRemoved.addListener(function (tabId) {
  if (tabId === martCachedWorkerTabId) {
    martCachedWorkerTabId = null;
  }
});

async function ensureMuseWorkerTab(switchedAccount) {
  // 1. Tái sử dụng tab worker đã mở trong cùng một phiên làm việc (tránh tạo tab mới hoặc tải lại thread cũ giữa các câu hỏi)
  if (martCachedWorkerTabId !== null) {
    try {
      var cachedTab = await chrome.tabs.get(martCachedWorkerTabId);
      if (cachedTab && cachedTab.id && cachedTab.url && cachedTab.url.startsWith("https://muse.ai/")) {
        if (switchedAccount) {
          await chrome.tabs.update(cachedTab.id, { url: "https://muse.ai/#mart_worker" });
          await waitForTabComplete(cachedTab.id, 18000);
        }
        return cachedTab.id;
      }
    } catch (_) {
      martCachedWorkerTabId = null;
    }
  }

  // 2. Tìm tab hiện có mang hash #mart_worker
  var tabs = await chrome.tabs.query({ url: "https://muse.ai/*" });
  var candidates = (tabs || []).filter(function (tab) {
    return tab && tab.id && (tab.url || "").includes("#mart_worker");
  });
  var candidateTab = candidates[0] || null;

  if (candidateTab) {
    martCachedWorkerTabId = candidateTab.id;
    var tabUrl = candidateTab.url || "";
    var isReady = tabUrl.startsWith("https://muse.ai/") && candidateTab.status === "complete";
    if (switchedAccount || !isReady) {
      await chrome.tabs.update(candidateTab.id, { url: "https://muse.ai/#mart_worker" });
      await waitForTabComplete(candidateTab.id, 18000);
    }
    return candidateTab.id;
  }

  // 3. Nếu chưa có tab worker nào, tạo mới và lưu ID
  var created = await chrome.tabs.create({
    url: "https://muse.ai/#mart_worker",
    active: false,
    pinned: true,
  });
  martCachedWorkerTabId = created.id;
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

  // Bộ chọn toàn diện cho mọi phiên bản Muse AI (React, Lexical, Slate, Draft, textarea native, contenteditable)
  function findCurrentMuseComposer() {
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
    var valid = all.filter(function (item) {
      if (item.disabled) return false;
      var ph = (item.getAttribute("placeholder") || "").toLowerCase();
      var al = (item.getAttribute("aria-label") || "").toLowerCase();
      if (ph.includes("tìm kiếm") || ph.includes("search") || al.includes("search")) {
        return false;
      }
      var style = window.getComputedStyle(item);
      if (style.display === "none" || style.visibility === "hidden") return false;
      var rect = item.getBoundingClientRect();
      // Chấp nhận cả element trong background tab khi layout chưa tính xong (offsetParent hoặc visible style)
      return (rect.width > 0 && rect.height > 0) || item.offsetParent !== null || style.opacity !== "0";
    });
    if (valid.length === 0) return null;
    valid.sort(function (a, b) {
      var aLabel = ((a.getAttribute("aria-label") || "") + " " + (a.getAttribute("placeholder") || "") + " " + (a.className || "")).toLowerCase();
      var bLabel = ((b.getAttribute("aria-label") || "") + " " + (b.getAttribute("placeholder") || "") + " " + (b.className || "")).toLowerCase();
      var aScore = (/message|chat|nhắn tin|tin nhắn|composer|prompt/i.test(aLabel) ? 100000 : 0) + (a.getBoundingClientRect().bottom || 0);
      var bScore = (/message|chat|nhắn tin|tin nhắn|composer|prompt/i.test(bLabel) ? 100000 : 0) + (b.getBoundingClientRect().bottom || 0);
      return bScore - aScore;
    });
    return valid[0];
  }

  var composer = findCurrentMuseComposer();
  var compVal = composer ? ((composer.value !== undefined ? composer.value : composer.textContent) || "").trim() : "";
  var isGen = !!document.querySelector('.animate-spin, button[aria-label*="Stop" i], button[aria-label*="Dừng" i], button.stop-button, [data-is-generating="true"], .typing-indicator, .pulse, button:has(span[class*="rounded-sm"]), .rounded-sm.bg-fill-primary');

  // Hàm loại trừ triệt để toàn bộ phần tử thuộc Sidebar, Drawer, Menu, Navigation, Lịch sử cũ
  function isIgnoredContainer(el) {
    if (!el || el.nodeType !== Node.ELEMENT_NODE) return true;
    if (el.closest('nav, aside, header, footer, [role="navigation"], [aria-label*="sidebar" i], [class*="sidebar" i], [data-testid*="sidebar" i], [class*="history" i], [id*="sidebar" i], [aria-label*="Lịch sử" i], [aria-label*="threads" i]')) {
      return true;
    }
    if (el.closest('form, textarea, input, [contenteditable="true"]')) {
      return true;
    }
    return false;
  }

  // 1. Tìm các phần tử chứa câu trả lời của trợ lý (Assistant) - CHỈ trong khung chat chính
  var assistantSelectors = [
    '[data-hatch-assistant-message-body="true"]',
    '[data-pel-click="assistant_message_content"]',
    'div[data-message-author-role="assistant"]',
    'div[class*="message-assistant"]',
    '.chat-bubble-assistant',
    'div[data-testid*="assistant-message"]',
    'div[data-role="assistant"]',
    '.prose'
  ];
  var rawEls = Array.from(document.querySelectorAll(assistantSelectors.join(", "))).filter(function (el) {
    if (isIgnoredContainer(el)) return false;
    if (el.closest('.bg-chat-user-bubble, div.items-end, [data-message-author-role="user"]')) return false;
    return true;
  });
  var topContainers = rawEls.filter(function (el) {
    return el.matches('div[data-message-author-role="assistant"], div[class*="message-assistant"], .chat-bubble-assistant, div[data-testid*="assistant-message"], div[data-role="assistant"]');
  });
  var targetEls = topContainers.length > 0 ? topContainers : rawEls;

  // 2. Tìm phần tử tin nhắn của User của lượt prompt hiện tại
  var expectedPrompt = String(window.__martActivePrompt || window.__martExpectedPrompt || "").trim();
  var expectedNorm = expectedPrompt.replace(/\s+/g, " ").toLowerCase();
  var expectedSnippet = expectedNorm.length > 25 ? expectedNorm.slice(0, 25) : expectedNorm;
  var userTurnEl = null;
  if (expectedSnippet) {
    var userCandidates = Array.from(document.querySelectorAll(
      '[data-message-author-role="user"], .bg-chat-user-bubble, [data-pel-click="user_message_content"], div[class*="chat-bubble-user"], div[class*="message-user"], div.items-end .hatch-chat-groupable-bubble, div.items-end, div[class*="user"], .hatch-chat-groupable-bubble, p, span'
    )).filter(function (el) {
      if (isIgnoredContainer(el)) return false;
      if (el.closest('[data-message-author-role="assistant"], div[class*="message-assistant"], .chat-bubble-assistant, div[data-testid*="assistant-message"], div[data-role="assistant"], [data-hatch-assistant-message-body="true"], .prose')) return false;
      var ut = (el.innerText || el.textContent || "").replace(/\s+/g, " ").trim().toLowerCase();
      return ut && ut.indexOf(expectedSnippet) >= 0;
    });
    if (userCandidates.length > 0) {
      userTurnEl = userCandidates[userCandidates.length - 1];
    }
  }

  // 3. Trích xuất danh sách markdown assistant & khối nằm SAU User message
  var mdList = [];
  var postUserMdList = [];
  for (var i = 0; i < targetEls.length; i++) {
    var m = nodeToMd(targetEls[i], 0).replace(/\n{3,}/g, "\n\n").trim();
    if (!m) {
      m = (targetEls[i].innerText || targetEls[i].textContent || "").trim();
    }
    if (m.length > 0) {
      mdList.push(m);
      if (userTurnEl) {
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
  return {
    hasComposer: !!composer,
    composerValue: compVal,
    isGenerating: isGen,
    userTurnSent: !!userTurnEl,
    count: count,
    lastMd: lastMd,
    unseenCount: unseenCount,
    unseenLastMd: unseenLastMd,
    url: location.href,
  };
}
}

// Hàm chạy bên trong trang https://muse.ai: điền prompt vào composer và nhấn gửi (kèm polling retry thông minh)
function domSubmitMusePrompt(promptText) {
  return new Promise(function (resolve) {
    var maxWaitMs = 12000;
    var startWait = Date.now();

    function pollAndSubmit() {
      var curUrl = location.href || "";
      if (curUrl.includes("/login") || curUrl.includes("/oidc/start") || curUrl.includes("/auth")) {
        resolve({ ok: false, error: "Phiên đăng nhập Muse.ai đã hết hạn hoặc trang yêu cầu đăng nhập lại." });
        return;
      }

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
      var valid = all.filter(function (item) {
        if (item.disabled) return false;
        var ph = (item.getAttribute("placeholder") || "").toLowerCase();
        var al = (item.getAttribute("aria-label") || "").toLowerCase();
        if (ph.includes("tìm kiếm") || ph.includes("search") || al.includes("search")) return false;
        var style = window.getComputedStyle(item);
        if (style.display === "none" || style.visibility === "hidden") return false;
        var rect = item.getBoundingClientRect();
        return (rect.width > 0 && rect.height > 0) || item.offsetParent !== null || style.opacity !== "0";
      });

      if (valid.length === 0) {
        if (Date.now() - startWait > maxWaitMs) {
          resolve({ ok: false, error: "Không tìm thấy ô nhập tin nhắn trên muse.ai (vui lòng đảm bảo bạn đã đăng nhập và trang chat đã tải xong)." });
          return;
        }
        setTimeout(pollAndSubmit, 300);
        return;
      }

      valid.sort(function (a, b) {
        var aLabel = ((a.getAttribute("aria-label") || "") + " " + (a.getAttribute("placeholder") || "") + " " + (a.className || "")).toLowerCase();
        var bLabel = ((b.getAttribute("aria-label") || "") + " " + (b.getAttribute("placeholder") || "") + " " + (b.className || "")).toLowerCase();
        var aScore = (/message|chat|nhắn tin|tin nhắn|composer|prompt/i.test(aLabel) ? 100000 : 0) + (a.getBoundingClientRect().bottom || 0);
        var bScore = (/message|chat|nhắn tin|tin nhắn|composer|prompt/i.test(bLabel) ? 100000 : 0) + (b.getBoundingClientRect().bottom || 0);
        return bScore - aScore;
      });

      var el = valid[0];
      window.__martActivePrompt = String(promptText || "").trim();
      window.__martExpectedPrompt = window.__martActivePrompt;

      try {
        el.focus();
      } catch (_) {}

      if (el.tagName === "TEXTAREA" || el.tagName === "INPUT") {
        var proto = el.tagName === "TEXTAREA" ? window.HTMLTextAreaElement.prototype : window.HTMLInputElement.prototype;
        var desc = Object.getOwnPropertyDescriptor(proto, "value");
        if (desc && desc.set) {
          desc.set.call(el, promptText);
        } else {
          el.value = promptText;
        }
        if (el._valueTracker) {
          try { el._valueTracker.setValue(''); } catch (_) {}
        }
        el.dispatchEvent(new InputEvent("input", { bubbles: true, data: promptText, inputType: "insertText" }));
        el.dispatchEvent(new Event("change", { bubbles: true }));
      } else {
        try {
          var sel = window.getSelection();
          var range = document.createRange();
          range.selectNodeContents(el);
          sel.removeAllRanges();
          sel.addRange(range);
          document.execCommand("insertText", false, promptText);
        } catch (_) {}
        if ((el.textContent || "").trim() !== promptText.trim()) {
          el.textContent = promptText;
        }
        el.dispatchEvent(new InputEvent("input", { bubbles: true, data: promptText, inputType: "insertText" }));
        el.dispatchEvent(new Event("input", { bubbles: true }));
      }

      setTimeout(function () {
        function tryClickSendButton() {
          var directBtn = document.querySelector('button[data-pel-click="chat_send_message"]:not(:disabled), button[aria-label="Gửi"]:not(:disabled), button[aria-label*="Gửi" i]:not(:disabled), button[aria-label="Send"]:not(:disabled), button[aria-label*="Send" i]:not(:disabled), button[data-testid*="send" i]:not(:disabled), button[type="submit"]:not(:disabled)');
          if (directBtn) {
            directBtn.click();
            return true;
          }
          var root = el.parentElement;
          for (var d = 0; d < 6 && root; d++) {
            var sendBtn = root.querySelector(
              'button[data-pel-click="chat_send_message"]:not(:disabled), button[aria-label*="Gửi" i]:not(:disabled), button[aria-label*="Send" i]:not(:disabled), button[data-testid*="send" i]:not(:disabled), button[type="submit"]:not(:disabled), button:has(svg)'
            );
            if (sendBtn) {
              sendBtn.disabled = false;
              sendBtn.click();
              return true;
            }
            root = root.parentElement;
          }
          if (el.form) {
            try { el.form.requestSubmit(); return true; } catch (_) {}
          }
          return false;
        }
        function pressEnterOnEl() {
          var evOpts = { key: "Enter", code: "Enter", keyCode: 13, which: 13, bubbles: true, cancelable: true };
          el.dispatchEvent(new KeyboardEvent("keydown", evOpts));
          el.dispatchEvent(new KeyboardEvent("keypress", evOpts));
          el.dispatchEvent(new KeyboardEvent("keyup", evOpts));
        }

        var clicked = tryClickSendButton();
        if (!clicked) {
          pressEnterOnEl();
        }
        setTimeout(function () {
          var remaining = (el.value !== undefined ? el.value : el.textContent || "").trim();
          if (remaining.length > 0) {
            if (!tryClickSendButton()) {
              pressEnterOnEl();
            }
            setTimeout(function () {
              var rem2 = (el.value !== undefined ? el.value : el.textContent || "").trim();
              if (rem2.length > 0) {
                resolve({ ok: false, error: "Muse.ai chưa nhận lệnh gửi (nội dung vẫn còn trong ô chat)." });
                return;
              }
              resolve({ ok: true });
            }, 450);
            return;
          }
          resolve({ ok: true });
        }, 400);
      }, 200);
    }

    pollAndSubmit();
  });
}

async function evalInTab(tabId, func, args) {
  var results = await withTimeout(
    chrome.scripting.executeScript({
      target: { tabId: tabId },
      func: func,
      args: args || [],
    }),
    10000,
    "Muse tab không phản hồi lệnh điều khiển."
  );
  return results && results[0] ? results[0].result : null;
}

async function executeMuseChatDirect(prompt, cookieHeader, forceReload) {
  var startTs = Date.now();
  var switched = await applyRelayAccountCookies(cookieHeader || "");
  var tabId = await ensureMuseWorkerTab(switched || Boolean(forceReload));

  // Chờ composer sẵn sàng & lịch sử .prose qua Noise WebSocket tải xong hoàn toàn
  var baseline = null;
  var prevCnt = -1;
  var prevMd = "";
  var stableBaseTicks = 0;
  for (var w = 0; w < 40; w++) {
    var snap = await evalInTab(tabId, domReadMuseSnapshot, []);
    if (snap && snap.hasComposer) {
      baseline = snap;
      if (snap.count > 0) {
        if (snap.count === prevCnt && snap.lastMd === prevMd) {
          stableBaseTicks += 1;
          if (stableBaseTicks >= 2) {
            break;
          }
        } else {
          stableBaseTicks = 0;
        }
      } else if (w >= 18) {
        break;
      }
      prevCnt = snap.count;
      prevMd = snap.lastMd;
    }
    await sleepMs(250);
  }

  var sendRes = null;
  if (!baseline || !baseline.hasComposer) {
    // Thử gửi trực tiếp vì domSubmitMusePrompt có cơ chế smart polling 12 giây
    sendRes = await evalInTab(tabId, domSubmitMusePrompt, [prompt]);
    if (!sendRes || !sendRes.ok) {
      return {
        ok: false,
        error: (sendRes && sendRes.error) || "Phiên đăng nhập muse.ai đã hết hạn hoặc trang chưa sẵn sàng — hãy bấm Đồng bộ lại tài khoản.",
      };
    }
  } else {
    sendRes = await evalInTab(tabId, domSubmitMusePrompt, [prompt]);
    if (!sendRes || !sendRes.ok) {
      return { ok: false, error: (sendRes && sendRes.error) || "Không gửi được tin nhắn vào muse.ai." };
    }
  }

  var deadline = Date.now() + 180000;
  var lastNewMd = "";
  var stableTicks = 0;
  var cleanPrompt = (prompt || "").trim();
  var noUserTurnTicks = 0;
  var refillAttempts = 0;

  while (Date.now() < deadline) {
    await sleepMs(260);
    var cur = await evalInTab(tabId, domReadMuseSnapshot, []);
    if (!cur) continue;

    // Tự động gia hạn deadline nếu Muse đang tiếp tục sinh nội dung (tạo video / render dự án / streaming)
    if (cur.isGenerating) {
      if (deadline - Date.now() < 35000) {
        deadline = Date.now() + 45000;
      }
    }

    var replyMd = "";
    // NGUYÊN TẮC BẤT DI BẤT DỊCH:
    // 1. BẮT BUỘC phải thấy tin nhắn của User xuất hiện trong khung chat chính (cur.userTurnSent)
    // 2. CHỈ trích xuất khối Assistant nằm SAU tin nhắn của User (cur.unseenLastMd)
    // 3. TUYỆT ĐỐI KHÔNG fallback bừa bãi lấy cur.lastMd của thread/lịch sử cũ!
    if (cur.userTurnSent && cur.unseenLastMd && cur.unseenLastMd !== cleanPrompt) {
      replyMd = (cur.unseenLastMd || "").trim();
    } else if (!cur.userTurnSent && baseline && cur.count > baseline.count && cur.lastMd && cur.lastMd !== baseline.lastMd && cur.lastMd !== cleanPrompt) {
      // Dự phòng an toàn: BẮT BUỘC số lượng khối Assistant phải tăng lên so với baseline (count > baseline.count)
      // VÀ nội dung tin nhắn cuối cùng phải KHÁC HOÀN TOÀN so với tin nhắn cuối trước khi gửi (lastMd !== baseline.lastMd)
      replyMd = (cur.lastMd || "").trim();
    } else {
      noUserTurnTicks++;
      // Nếu sau ~1.8 giây (7 nhịp) chưa thấy User Turn xuất hiện hoặc composer còn dính text:
      if (noUserTurnTicks >= 7 && refillAttempts < 4) {
        noUserTurnTicks = 0;
        refillAttempts++;
        await evalInTab(tabId, domSubmitMusePrompt, [prompt]);
      }
      continue;
    }

    if (replyMd === lastNewMd) {
      stableTicks += 1;
      var reqTicks = cur.isGenerating ? 7 : 3;
      if (stableTicks >= reqTicks) {
        break;
      }
    } else {
      lastNewMd = replyMd;
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

function enqueueMuseChat(prompt, cookieHeader, forceReload) {
  function executeWithDeadline() {
    return withTimeout(
      executeMuseChatDirect(prompt, cookieHeader, forceReload),
      195000,
      "Hết thời gian chờ Muse hoàn tất tác vụ (tác vụ tạo video/web có thể cần nhiều thời gian hơn); hãy thử lại."
    );
  }
  var next = chatLockPromise.then(
    function () {
      return executeWithDeadline();
    },
    function () {
      return executeWithDeadline();
    }
  );
  chatLockPromise = next.catch(function () {});
  return next;
}

function handleBridgeAction(msg, sendResponse) {
  var action = (msg && msg.action) || "";
  if (action === "ping") {
    martCollectMuseCookies().then(function (c) {
      sendResponse({ ok: true, version: "1.2.8", hasExistingSession: !!c.sess, supportsChatRelay: true });
    });
    return true;
  }
  if (action === "ping_with_cookie") {
    martCollectMuseCookies().then(function (c) {
      sendResponse({
        ok: true,
        version: "1.2.8",
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
    enqueueMuseChat(msg.prompt || "", msg.cookie || "", Boolean(msg.forceReload))
      .then(sendResponse)
      .catch(function (err) {
        sendResponse({ ok: false, error: (err && err.message) || "Lỗi thực thi trên tab muse.ai" });
      });
    return true;
  }
  if (action === "open_native_muse") {
    (async function () {
      try {
        if (msg.cookie) {
          await applyRelayAccountCookies(msg.cookie);
        }
        var targetUrl = msg.url || "https://muse.ai/";
        await chrome.tabs.create({ url: targetUrl, active: true });
        sendResponse({ ok: true, opened: true });
      } catch (err) {
        sendResponse({ ok: false, error: (err && err.message) || "Không thể mở tab muse.ai" });
      }
    })();
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
