// Hello Panel — the reference Multica surface.
//
// Plain ES module, no build step: the point is to show the contract, not a
// toolchain. It runs in a sandboxed iframe with an opaque origin, so there is
// no cookie, no localStorage and no access to the page around it. Everything it
// does goes through the host bridge.
//
// In a real plugin you would `import { multica } from "@multica/plugin-sdk"`.
// This file inlines a minimal client so it can be served as a single static
// file with nothing to install.

const pending = new Map();
const port = globalThis.__multicaPluginBridgePortV2;
let sequence = 0;
let cachedContext = null;
let latestFlags = null;
let flagsRevision = 0;
const flagListeners = new Set();

if (!(port instanceof MessagePort)) throw new Error("Multica surface bridge is unavailable");
delete globalThis.__multicaPluginBridgePortV2;
port.onmessage = (message) => {
  const payload = message.data;
  if (payload?.kind === "theme") return applyTheme(payload.theme);
  if (payload?.kind === "flags") {
    latestFlags = { ...payload.flags };
    flagsRevision++;
    if (cachedContext) cachedContext = { ...cachedContext, flags: { ...latestFlags } };
    for (const listener of flagListeners) listener({ ...latestFlags });
    return;
  }
  const entry = pending.get(payload?.id);
  if (!entry) return;
  pending.delete(payload.id);
  if (payload.ok) entry.resolve(payload.data);
  else entry.reject(new Error(payload.error));
};
port.start();

function applyTheme(theme) {
  for (const [name, value] of Object.entries(theme ?? {})) {
    document.documentElement.style.setProperty(name, value);
  }
}

function call(method, path, body) {
  const id = `r${++sequence}`;
  return new Promise((resolve, reject) => {
    pending.set(id, { resolve, reject });
    port.postMessage({ id, kind: "action", method, path, body });
  });
}

// The same read/subscription shape as @multica/plugin-sdk 0.2.0. This small
// inline client keeps the example publishable without a bundling step.
const multica = {
  context: {
    get: async (force = false) => {
      if (cachedContext && !force) return cachedContext;
      const revision = flagsRevision;
      const context = await call("GET", "/context");
      if (latestFlags === null || (force && revision === flagsRevision)) {
        latestFlags = { ...(context.flags ?? {}) };
      }
      cachedContext = { ...context, flags: { ...latestFlags } };
      return cachedContext;
    },
  },
  flags: {
    all: async () => {
      if (latestFlags === null) await multica.context.get();
      return { ...latestFlags };
    },
    get: async (key, fallback = false) => {
      return (await multica.flags.all())[key] ?? fallback;
    },
    onChange(listener) {
      flagListeners.add(listener);
      if (latestFlags !== null) listener({ ...latestFlags });
      return () => flagListeners.delete(listener);
    },
  },
};

function resize() {
  // The host clamps this; asking for the content height is the normal case.
  port.postMessage({ id: `resize${Date.now()}`, kind: "ui.resize", height: document.body.scrollHeight + 16 });
}

const NOTE_KEY = "note";

async function start() {
  const root = document.getElementById("root");
  root.innerHTML = `
    <div style="padding:12px 14px; display:grid; gap:10px">
      <div id="who" style="color:var(--muted-foreground)">Loading…</div>
      <div id="title" style="font-weight:600"></div>
      <div style="display:flex; gap:8px">
        <input id="note" placeholder="A note only you can see"
               style="flex:1; padding:6px 8px; border:1px solid var(--border); border-radius:var(--radius,6px);
                      background:var(--background); color:var(--foreground)">
        <button id="save" style="padding:6px 10px">Save</button>
      </div>
      <p id="hint" data-testid="feature-hint" hidden
         style="margin:0; color:var(--muted-foreground)">Your note is private to you in this workspace.</p>
      <button id="comment" style="padding:6px 10px; justify-self:start">Post a test comment</button>
      <div id="status" style="color:var(--muted-foreground); min-height:1.2em"></div>
    </div>`;

  const status = (text) => { root.querySelector("#status").textContent = text; resize(); };

  try {
    const context = await multica.context.get();
    const renderHint = (flags) => {
      root.querySelector("#hint").hidden = flags.show_hint !== true;
      resize();
    };
    renderHint(await multica.flags.all());
    root.dataset.contextShowHint = String(context.flags.show_hint ?? false);
    root.dataset.flagChanges = "0";
    const unsubscribe = multica.flags.onChange(async (flags) => {
      renderHint(flags);
      root.dataset.flagChanges = String(Number(root.dataset.flagChanges) + 1);
      // The cached context follows pushes too, without another Action request.
      const updatedContext = await multica.context.get();
      root.dataset.contextShowHint = String(updatedContext.flags.show_hint ?? false);
    });
    window.addEventListener("pagehide", unsubscribe, { once: true });
    root.querySelector("#who").textContent =
      `${context.user.name} · ${context.workspace.name}` + (context.issue ? ` · ${context.issue.identifier}` : "");

    const issue = await call("GET", `/issues/${encodeURIComponent(context.issue.id)}`);
    root.querySelector("#title").textContent = issue.title;

    // storage:user — per member, so another member opening the same panel on
    // the same issue sees their own note, not this one.
    const saved = await call("GET", `/storage/user/${NOTE_KEY}`).catch(() => null);
    if (saved) root.querySelector("#note").value = saved.value;

    root.querySelector("#save").onclick = async () => {
      try {
        await call("PUT", `/storage/user/${NOTE_KEY}`, { value: root.querySelector("#note").value });
        status("Saved.");
      } catch (error) {
        status(error.message);
      }
    };

    root.querySelector("#comment").onclick = async () => {
      try {
        // Lands as the signed-in user, marked with this plugin. If the admin
        // did not grant comments:write, this is a 403 that names the scope.
        await call("POST", `/issues/${encodeURIComponent(context.issue.id)}/comments`, {
          content: "Posted from Hello Panel.",
        });
        status("Comment posted.");
      } catch (error) {
        status(error.message);
      }
    };
  } catch (error) {
    status(error.message);
  }
  resize();
}

start();
