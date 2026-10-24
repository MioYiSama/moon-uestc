(() => {
  const terminalStates = new Set(["completed", "unbound", "cancelled", "expired", "failed"]);
  const labels = {
    pending: "等待扫码",
    scanned: "已扫码",
    authorized: "已授权",
    checking_bind: "检查绑定",
    completed: "已完成",
    unbound: "未绑定",
    cancelled: "已取消",
    expired: "已过期",
    failed: "未完成",
  };
  const tones = {
    completed: "positive",
    unbound: "warning",
    failed: "negative",
    expired: "neutral",
    cancelled: "neutral",
  };

  document.addEventListener("submit", (event) => {
    const form = event.target.closest("form[data-confirm]");
    if (form && !window.confirm(form.dataset.confirm || "确认继续此操作？")) {
      event.preventDefault();
    }
  });

  document.addEventListener("submit", (event) => {
    const form = event.target.closest("form[data-checkin]");
    if (!form || event.defaultPrevented) return;
    const button = form.querySelector('button[type="submit"]');
    if (button.disabled) {
      event.preventDefault();
      return;
    }
    button.disabled = true;
    button.textContent = "签到中…";
    form.setAttribute("aria-busy", "true");
  });
  window.addEventListener("pageshow", () => {
    document.querySelectorAll("form[data-checkin]").forEach((form) => {
      const button = form.querySelector('button[type="submit"]');
      button.disabled = false;
      button.textContent = "手动签到";
      form.removeAttribute("aria-busy");
    });
  });

  const state = document.querySelector("#scan-state");
  if (!state || state.dataset.terminal === "true") return;

  const label = document.querySelector("#scan-status-label");
  const message = document.querySelector("#scan-live-message");
  const accountLink = document.querySelector("#scan-account-link");
  const qrImage = state.querySelector(".qr-image");
  if (qrImage) {
    qrImage.addEventListener("error", () => {
      if (message) message.textContent = "二维码暂不可用，请取消本次扫码后重新创建。";
    }, { once: true });
  }
  let timer = 0;
  let stopped = false;

  const stop = () => {
    stopped = true;
    window.clearTimeout(timer);
    state.dataset.terminal = "true";
    const image = state.querySelector(".qr-stage");
    const cancelForm = state.querySelector(".scan-cancel-form");
    if (image) image.hidden = true;
    if (cancelForm) cancelForm.hidden = true;
  };

  const update = (snapshot) => {
    const status = labels[snapshot.status] ? snapshot.status : "failed";
    if (label) {
      label.textContent = labels[status];
      label.className = `status-mark status-${tones[status] || "neutral"}`;
      state.dataset.status = status;
    }
    if (message && typeof snapshot.message === "string") {
      message.textContent = snapshot.message;
    }
    if (accountLink && typeof snapshot.accountUrl === "string" && /^\/accounts\/[1-9][0-9]*$/.test(snapshot.accountUrl)) {
      accountLink.href = snapshot.accountUrl;
      accountLink.textContent = "查看账号详情";
      accountLink.hidden = false;
    }
    if (snapshot.terminal === true || terminalStates.has(status)) stop();
  };

  const poll = async () => {
    if (stopped) return;
    try {
      const response = await fetch(state.dataset.scanStatusUrl, {
        method: "GET",
        cache: "no-store",
        credentials: "same-origin",
        headers: { Accept: "application/json" },
      });
      if (response.redirected || !response.headers.get("content-type")?.includes("application/json")) {
        update({ status: "failed", message: "管理员会话已失效，请重新登录。", terminal: true });
        return;
      }
      if (response.status === 404 || response.status === 410) {
        const snapshot = await response.json().catch(() => ({}));
        update({ status: response.status === 410 ? "expired" : "failed", message: snapshot.message || "扫码会话已结束。", terminal: true });
        return;
      }
      if (response.ok) {
        update(await response.json());
        if (stopped) return;
      }
    } catch {
      // A temporary read failure does not change the upstream scan state.
    }
    if (!stopped) timer = window.setTimeout(poll, 2000);
  };

  timer = window.setTimeout(poll, 2000);
  window.addEventListener("pagehide", () => {
    stopped = true;
    window.clearTimeout(timer);
  }, { once: true });
})();
