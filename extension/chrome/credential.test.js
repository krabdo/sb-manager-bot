const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

global.TextEncoder = require("node:util").TextEncoder;
global.btoa = (value) => Buffer.from(value, "binary").toString("base64");
const api = require("./credential.js");

test("accepts only an exact sb.sb notification URL", () => {
  assert.equal(api.parseNotificationURL("https://sb.sb/u/1777/?tab=notifications").forumUserID, "1777");
  assert.equal(api.parseNotificationURL("https://sb.sb/u/1777/?tab=posts"), null);
  assert.equal(api.parseNotificationURL("https://evil.example/u/1777/?tab=notifications"), null);
  assert.equal(api.parseNotificationURL("http://sb.sb/u/1777/?tab=notifications"), null);
});

test("assembles all applicable cookies in path-priority order", () => {
  const page = api.parseNotificationURL("https://sb.sb/u/1777/?tab=notifications");
  const cookies = [
    { name: "sid", value: "root", domain: ".sb.sb", path: "/", secure: true },
    { name: "sid", value: "user", domain: "sb.sb", path: "/u/", secure: true, httpOnly: true },
    { name: "theme", value: "dark", domain: "sb.sb", path: "/" },
    { name: "other", value: "no", domain: "example.com", path: "/" },
  ];
  assert.equal(api.buildCookieHeader(cookies, page), "sid=user; sid=root; theme=dark");
});

test("encodes UTF-8 SBM1 payload and enforces the size limit", () => {
  const value = api.encodeCredential("1777", "session=中文");
  const decoded = JSON.parse(Buffer.from(value.slice(5), "base64url").toString("utf8"));
  assert.deepEqual(decoded, { v: 1, forum_user_id: "1777", cookie: "session=中文" });
  assert.throws(() => api.encodeCredential("1777", "x=" + "a".repeat(api.MAX_CREDENTIAL_BYTES)), /16 KiB/);
});

test("manifest is minimum-scope and extension contains no network calls", () => {
  const directory = __dirname;
  const manifest = JSON.parse(fs.readFileSync(path.join(directory, "manifest.json"), "utf8"));
  assert.deepEqual(manifest.permissions.sort(), ["activeTab", "clipboardWrite", "cookies"]);
  assert.deepEqual(manifest.host_permissions, ["https://sb.sb/*"]);
  assert.equal(manifest.background, undefined);
  assert.equal(manifest.content_scripts, undefined);
  const source = ["credential.js", "popup.js"].map((file) => fs.readFileSync(path.join(directory, file), "utf8")).join("\n");
  assert.doesNotMatch(source, /XMLHttpRequest|GM_xmlhttpRequest|\bfetch\s*\(|chrome\.storage|localStorage/);
});
