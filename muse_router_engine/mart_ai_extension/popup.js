/* Mart AI Extension — Popup UI */
"use strict";

(function () {
  var el = {
    server: document.getElementById("server"),
    password: document.getElementById("password"),
    label: document.getElementById("label"),
    auto: document.getElementById("auto"),
    send: document.getElementById("send"),
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
      showStatus("🟢 Đã phát hiện phiên muse.ai đang đăng nhập trên trình duyệt này.", "ok");
    }
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
