/* Mart AI Extension — Logic dùng chung cho popup và background service worker */
"use strict";

var MART_DEFAULT_SERVER = "https://mart-ai.onrender.com";

function martNormalizeServer(raw) {
  var s = (raw || "").trim().replace(/\/+$/, "");
  if (!s) return "";
  if (!/^https?:\/\//i.test(s)) s = "https://" + s;
  var u;
  try {
    u = new URL(s);
  } catch (_) {
    return "";
  }
  var isLocal = u.hostname === "127.0.0.1" || u.hostname === "localhost";
  if (u.protocol !== "https:" && !isLocal) return "";
  return u.origin;
}

function martLoadConfig() {
  return new Promise(function (resolve) {
    chrome.storage.local.get(
      { server: MART_DEFAULT_SERVER, password: "", label: "", auto: false, lastSess: "" },
      resolve
    );
  });
}

function martSaveConfig(patch) {
  return new Promise(function (resolve) {
    chrome.storage.local.set(patch, resolve);
  });
}

// Đọc toàn bộ cookie muse.ai (kể cả HttpOnly hatch_sess nhờ quyền "cookies" của extension)
function martCollectMuseCookies() {
  return new Promise(function (resolve) {
    chrome.cookies.getAll({ domain: "muse.ai" }, function (cookies) {
      var byName = {};
      (cookies || []).forEach(function (c) {
        if (c && c.name && c.value) {
          byName[c.name] = c.value;
        }
      });
      var names = Object.keys(byName);
      resolve({
        header: names
          .map(function (k) {
            return k + "=" + byName[k];
          })
          .join("; "),
        sess: byName["hatch_sess"] || "",
        hasGw: !!byName["hatch_gw"],
      });
    });
  });
}

// Xóa cookie phiên muse.ai cũ trên trình duyệt trước khi đăng nhập tài khoản thứ 2, thứ 3...
function martClearMuseSessionCookies() {
  return new Promise(function (resolve) {
    chrome.cookies.getAll({ domain: "muse.ai" }, function (cookies) {
      var list = cookies || [];
      if (!list.length) return resolve();
      var remaining = list.length;
      list.forEach(function (c) {
        var host = (c.domain || "muse.ai").replace(/^\./, "");
        var url = (c.secure ? "https://" : "http://") + host + (c.path || "/");
        chrome.cookies.remove({ url: url, name: c.name }, function () {
          remaining -= 1;
          if (remaining <= 0) resolve();
        });
      });
    });
  });
}

// Gửi tài khoản trực tiếp về API /api/accounts/ingest bằng mật khẩu MART_ADMIN_PASSWORD
async function martSendAccount(cfg) {
  var server = martNormalizeServer(cfg.server);
  if (!server) {
    return { ok: false, message: "Địa chỉ Mart AI không hợp lệ (cần https://…)." };
  }
  if (!cfg.password) {
    return { ok: false, message: "Chưa nhập mật khẩu admin (MART_ADMIN_PASSWORD)." };
  }
  var data = await martCollectMuseCookies();
  if (!data.sess) {
    return { ok: false, message: "Chưa thấy phiên đăng nhập muse.ai — hãy đăng nhập muse.ai trước." };
  }

  var resp;
  try {
    resp = await fetch(server + "/api/accounts/ingest", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: "Bearer " + cfg.password,
      },
      body: JSON.stringify({ label: (cfg.label || "").trim(), cookie: data.header }),
    });
  } catch (_) {
    return {
      ok: false,
      message: "Không kết nối được tới " + server + " (nếu Render đang ngủ, hãy thử lại sau ~30 giây).",
    };
  }

  var body = null;
  try {
    body = await resp.json();
  } catch (_) {
    body = null;
  }
  if (resp.status === 401) return { ok: false, message: "Sai mật khẩu admin (MART_ADMIN_PASSWORD)." };
  if (resp.status === 503) {
    return {
      ok: false,
      message: "Máy chủ chưa đặt biến môi trường MART_ADMIN_PASSWORD trên Render (hoặc dùng nút Bắt Tự Động ngay trên trang /admin).",
    };
  }
  if (!resp.ok) {
    return {
      ok: false,
      message: "Máy chủ từ chối (" + resp.status + "): " + ((body && body.error) || "lỗi không xác định"),
    };
  }
  var node = (body && body.node) || {};
  var name = node.label || "tài khoản";
  var status =
    node.status === "ready"
      ? "đã đồng bộ thành công"
      : "đã lưu (" + (node.status_detail || node.status || "đang kiểm tra") + ")";
  return {
    ok: true,
    created: !!(body && body.created),
    sess: data.sess,
    message: (body && body.created ? "✅ Đã thêm " : "✅ Đã cập nhật ") + name + " — " + status + ".",
  };
}
