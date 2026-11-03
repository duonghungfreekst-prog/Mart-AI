/* Mart AI Extension — Service Worker:
   1) Giao tiếp 1 chạm với trang /admin của Mart AI (externally_connectable) để bắt thẳng hatch_sess tự động 100%
   2) Tự động đẩy về /api/accounts/ingest khi bật chế độ Tự động gửi trong Popup */
"use strict";

importScripts("common.js");

var pendingTimer = null;

function setBadge(text, color) {
  chrome.action.setBadgeText({ text: text });
  chrome.action.setBadgeBackgroundColor({ color: color });
}

async function autoSendIfNeeded() {
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

chrome.cookies.onChanged.addListener(function (change) {
  var c = change.cookie;
  if (!c || change.removed) return;
  if (c.name !== "hatch_sess" || !/(^|\.)muse\.ai$/.test(c.domain.replace(/^\./, ""))) return;
  if (pendingTimer) clearTimeout(pendingTimer);
  pendingTimer = setTimeout(function () {
    pendingTimer = null;
    autoSendIfNeeded();
  }, 3500);
});

// Mở tab đăng nhập chính thức của muse.ai, chờ hatch_sess xuất hiện và trả thẳng về trang Mart AI
async function interactiveCaptureFlow(options) {
  var opts = options || {};
  var existing = await martCollectMuseCookies();

  // Nếu người dùng bấm "Bắt phiên Muse đang mở" và đã có sẵn hatch_sess -> trả về ngay tức thì (0 giây)!
  if (opts.onlyExisting) {
    if (existing.sess) {
      setBadge("OK", "#16a34a");
      return { ok: true, cookie: existing.header, immediate: true };
    }
    return { ok: false, error: "Chưa có phiên muse.ai nào đang đăng nhập trên trình duyệt này." };
  }

  // Nếu chưa có phiên mà không yêu cầu đổi tài khoản mới, hoặc nếu người dùng muốn mở cửa sổ đăng nhập mới:
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
        // Kiểm tra lần cuối phòng trường hợp người dùng tự đóng tab sau khi đăng nhập xong
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

      // Kiểm tra tab đã quay về muse.ai (không còn ở auth.meta.com hay /oidc/error)
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

chrome.runtime.onMessageExternal.addListener(function (msg, _sender, sendResponse) {
  var action = (msg && msg.action) || "";
  if (action === "ping") {
    martCollectMuseCookies().then(function (c) {
      sendResponse({ ok: true, version: "1.0.0", hasExistingSession: !!c.sess });
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
  sendResponse({ ok: false, error: "UNKNOWN_ACTION" });
  return false;
});
