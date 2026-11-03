/* Mart AI Extension v1.2 — Popup UI (100% Tự động, không cần nhập form) */
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
            copyBtn.textContent = "✅ Đã copy! Dán lên thanh địa chỉ Chrome → Enter";
          });
        }
      });
    }
    return;
  }

  var pushBtn = document.getElementById("btnPushOpenTab");
  var statusEl = document.getElementById("status");

  function showStatus(text, kind) {
    if (!statusEl) return;
    statusEl.textContent = text;
    statusEl.className = kind || "";
  }

  async function init() {
    var cookies = await martCollectMuseCookies();
    if (cookies.sess) {
      showStatus("✅ Đã kết nối phiên muse.ai — tự động đồng bộ với Mart AI!", "ok");
      chrome.runtime.sendMessage({ action: "broadcast_now" }, function () {
        void chrome.runtime.lastError;
      });
    } else {
      showStatus("Chưa đăng nhập muse.ai. Bấm nút trên để mở trang đăng nhập.", "err");
    }
  }

  if (pushBtn) {
    pushBtn.addEventListener("click", async function () {
      var cookies = await martCollectMuseCookies();
      if (!cookies.sess) {
        chrome.tabs.create({ url: "https://muse.ai/api/oidc/start?return_to=%2F", active: true });
        return;
      }
      pushBtn.disabled = true;
      chrome.runtime.sendMessage({ action: "broadcast_now" }, function () {
        void chrome.runtime.lastError;
        pushBtn.disabled = false;
        showStatus("✅ Đã đẩy tài khoản sang tab Mart AI đang mở!", "ok");
      });
    });
  }

  init();
})();
