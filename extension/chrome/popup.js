(async function () {
  "use strict";

  const status = document.getElementById("status");
  const account = document.getElementById("account");
  const copyButton = document.getElementById("copy");
  const clearButton = document.getElementById("clear");
  let page = null;

  function show(message, kind) {
    status.textContent = message;
    status.className = "status" + (kind ? " " + kind : "");
  }

  async function currentTab() {
    const tabs = await chrome.tabs.query({ active: true, currentWindow: true });
    return tabs[0] || null;
  }

  async function initialize() {
    try {
      const tab = await currentTab();
      page = tab && SBMCredential.parseNotificationURL(tab.url || "");
      if (!page) {
        show("请先打开你自己的 sb.sb 通知页面，再点击扩展。", "error");
        return;
      }
      account.textContent = "检测到论坛 UID：" + page.forumUserID;
      account.hidden = false;
      copyButton.disabled = false;
      show("页面已就绪。点击下面的按钮生成凭据。", "success");
    } catch (_) {
      show("无法读取当前标签页，请关闭扩展后重试。", "error");
    }
  }

  copyButton.addEventListener("click", async () => {
    copyButton.disabled = true;
    clearButton.disabled = true;
    show("正在本地读取登录 Cookie…");
    try {
      const cookies = await chrome.cookies.getAll({ url: page.url });
      if (!cookies.some((cookie) => cookie.httpOnly)) throw new Error("未检测到 HttpOnly 登录会话，请先在 sb.sb 登录并刷新通知页。");
      const header = SBMCredential.buildCookieHeader(cookies, page);
      if (!header) throw new Error("没有找到适用于当前通知页的 Cookie。");
      const credential = SBMCredential.encodeCredential(page.forumUserID, header);
      await navigator.clipboard.writeText(credential);
      show("复制成功！请立即粘贴到 Bot 私聊。", "success");
      clearButton.disabled = false;
    } catch (error) {
      show(error && error.message ? error.message : "生成失败，请刷新通知页后重试。", "error");
    } finally {
      copyButton.disabled = false;
    }
  });

  clearButton.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText("");
      clearButton.disabled = true;
      show("剪贴板已清空。", "success");
    } catch (_) {
      show("无法清空剪贴板，请在系统剪贴板历史中手动删除。", "error");
    }
  });

  await initialize();
})();
