/* Mart AI Extension v1.2 — Popup UI */
"use strict";

(function () {
  var isExtensionContext =
    typeof chrome !== "undefined" &&
    !!chrome.cookies &&
    !!chrome.runtime &&
    window.location.protocol === "chrome-extension:";

  if (!isExtensionContext) {
    document.body.classList.add("file-mode");
    var warnEl = document.getElementById("notInstalledWarning");
    var panelEl = document.getElementById("installedPanel");
    if (warnEl) warnEl.style.display = "block";
    if (panelEl) panelEl.style.display = "none";

    var copyBtn = document.getElementById("btnCopyExtUrl");
    if (copyBtn) {
      copyBtn.addEventListener("click", function () {
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText("chrome://extensions").then(function () {
            copyBtn.textContent = "✅ Đã copy! Mở Tab mới → nhấn Ctrl+V → Enter";
          });
        }
      });
    }
    return;
  }

  var el = {
    server: document.getElementById("server"),
    password: document.getElementById("password"),
    label: document.getElementById("label"),
    auto: document.getElementById("auto"),
    send: document.getElementById("send"),
    pushTab: document.getElementById("btnPushOpenTab"),
    status: document.getElementById("status"),
  };

  function showStatus(text, kind) {
    el.status.textContent = text;
    el.status.className = kind || "";
  }

  function readForm() {
    return {
      server: el.server.value.trim(),
      password: el.password.value,
      label: el.label.value.trim(),
      auto: el.auto.checked,
    };
  }

  function ensureServerPermission(origin) {
    return new Promise(function (resolve) {
      var pattern = origin + "/*";
      chrome.permissions.contains({ origins: [pattern] }, function (has) {
        if (has) return resolve(true);
        chrome.permissions.request({ origins: [pattern] }, function (granted) {
          resolve(!!granted);
        });
      });
    });
  }

  async function init() {
    var cfg = await martLoadConfig();
    el.server.value = cfg.server || MART_DEFAULT_SERVER;
    el.password.value = cfg.password || "";
    el.label.value = cfg.label || "";
    el.auto.checked = !!cfg.auto;

    var cookies = await martCollectMuseCookies();
    if (cookies.sess) {
      showStatus("🟢 Đã phát hiện phiên muse.ai đang đăng nhập! Đang tự đồng bộ sang tab Mart AI...", "ok");
      chrome.runtime.sendMessage({ action: "broadcast_now" }, function () {
        void chrome.runtime.lastError;
      });
    } else {
      showStatus("⚠️ Chưa đăng nhập muse.ai trên trình duyệt này. Hãy đăng nhập tại muse.ai trước.", "err");
    }
  }

  if (el.pushTab) {
    el.pushTab.addEventListener("click", async function () {
      var cookies = await martCollectMuseCookies();
      if (!cookies.sess) {
        chrome.tabs.create({ url: "https://muse.ai/api/oidc/start?return_to=%2F", active: true });
        showStatus("Đã mở trang đăng nhập muse.ai — đăng nhập xong sẽ tự bắt ngay!", "ok");
        return;
      }
      el.pushTab.disabled = true;
      chrome.runtime.sendMessage({ action: "broadcast_now" }, function () {
        void chrome.runtime.lastError;
        el.pushTab.disabled = false;
        showStatus("✅ Đã đẩy phiên đăng nhập sang tab Mart AI đang mở!", "ok");
      });
    });
  }

  el.auto.addEventListener("change", function () {
    martSaveConfig({ auto: el.auto.checked });
  });

  el.send.addEventListener("click", async function () {
    var form = readForm();
    var origin = martNormalizeServer(form.server);
    if (!origin) {
      showStatus("Địa chỉ Mart AI không hợp lệ (cần https://…).", "err");
      return;
    }
    el.send.disabled = true;
    showStatus("⏳ Đang gửi phiên đăng nhập về Mart AI…");
    try {
      var granted = await ensureServerPermission(origin);
      if (!granted) {
        showStatus("Cần cấp quyền truy cập " + origin + " để gửi tài khoản.", "err");
        return;
      }
      await martSaveConfig({ server: origin, password: form.password, label: form.label, auto: form.auto });
      var res = await martSendAccount({ server: origin, password: form.password, label: form.label });
      if (res.ok) {
        await martSaveConfig({ lastSess: res.sess });
        chrome.action.setBadgeText({ text: "OK" });
        chrome.action.setBadgeBackgroundColor({ color: "#16a34a" });
      }
      showStatus(res.message, res.ok ? "ok" : "err");
    } finally {
      el.send.disabled = false;
    }
  });

  init();
})();
