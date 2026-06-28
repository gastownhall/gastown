// Gas Town OpenCode plugin.
// - SessionStart/Compaction: inject `gt prime --hook` context (system.transform).
// - TURN-BOUNDARY DRAIN (parity with Claude's UserPromptSubmit hook): on
//   `session.idle`, drain the gt nudge/mail queue via `gt mail check --inject`
//   and, if there is queued work, push it back into the session as a new prompt
//   through the OpenCode SDK client. This makes OpenCode self-driving WITHOUT
//   relying on gt's tmux nudge-poller (which is unreliable for OpenCode because
//   it has no detectable ready-prompt and no resume). Long-lived roles (mayor)
//   poll while idle so newly-queued work wakes them; one-shot roles (polecat)
//   only drain at the boundary and then let gt reap them.
export const GasTown = async ({ $, directory, client }) => {
  const role = (process.env.GT_ROLE || "").toLowerCase();
  const gtBin = process.env.GT_BIN || "gt";
  let didInit = false;

  let primePromise = null;

  const outputTail = (value, maxChars = 2000) => {
    if (value === undefined || value === null) return "<empty>";
    let text;
    if (typeof value === "string") text = value;
    else if (value instanceof Uint8Array) text = new TextDecoder().decode(value);
    else text = String(value);
    text = text.trimEnd();
    if (!text) return "<empty>";
    if (text.length <= maxChars) return text;
    return `...<truncated ${text.length - maxChars} chars>\n${text.slice(-maxChars)}`;
  };

  const exitCode = (err) => {
    const candidates = [err?.exitCode, err?.exit_code, err?.status, err?.code];
    for (const code of candidates) if (typeof code === "number") return code;
    return null;
  };

  const shellQuote = (value) => `'${String(value).replace(/'/g, `'\\''`)}'`;

  const gtCommand = () => {
    if (/^[A-Za-z0-9_./-]+$/.test(gtBin)) return gtBin;
    return shellQuote(gtBin);
  };

  const isDoltBackedCommand = (cmd) =>
    /(^|\s)(?:'[^']*\/|[^'\s]*\/)?(?:gt|bd)'?\s/.test(cmd) &&
    !/(^|\s)(?:'[^']*\/|[^'\s]*\/)?gt'?\s+dolt\s+status(\s|$)/.test(cmd);

  const captureDoltStatus = async () => {
    const statusCmd = `timeout 10s ${gtCommand()} dolt status 2>&1`;
    try {
      return await $`/bin/sh -lc ${statusCmd}`.cwd(directory).text();
    } catch (err) {
      return [
        `status_command: ${statusCmd}`,
        `status_error: ${err?.message || err}`,
        `status_stdout_tail: ${outputTail(err?.stdout)}`,
        `status_stderr_tail: ${outputTail(err?.stderr)}`,
      ].join("\n");
    }
  };

  const logFailure = async (cmd, err) => {
    const code = exitCode(err);
    const message = err?.message || String(err);
    const timeout = code === 124 || /exit code 124|timed?\s*out/i.test(message)
      ? "yes (exit code 124 / timeout)" : "not indicated";
    const lines = [
      "[gastown] command failed",
      `command: ${cmd}`,
      `exit_code: ${code ?? "unknown"}`,
      `timeout: ${timeout}`,
      `error: ${message}`,
      `stdout_tail: ${outputTail(err?.stdout)}`,
      `stderr_tail: ${outputTail(err?.stderr)}`,
    ];
    if (isDoltBackedCommand(cmd)) {
      lines.push(`dolt_status_tail:\n${outputTail(await captureDoltStatus())}`);
      lines.push("suggested_recovery: If Dolt is unhealthy or another gt/bd command is hanging, capture SIGQUIT and `gt dolt status` diagnostics before escalating; otherwise retry after the timeout clears.");
    } else {
      lines.push("suggested_recovery: Inspect the command, stdout/stderr tails, and retry once the timeout or process failure is resolved.");
    }
    console.error(lines.join("\n"));
  };

  const simpleRole = (value) => {
    if (!value) return "";
    const parts = value.split("/").filter(Boolean);
    if (parts.length >= 2 && parts[1] === "polecats") return "polecat";
    if (parts.length >= 2 && parts[1] === "crew") return "crew";
    if (parts.length >= 2) return parts[1];
    return parts[0];
  };

  const eventSessionID = (event) =>
    event?.properties?.sessionID ||
    event?.properties?.info?.id ||
    event?.sessionID ||
    event?.session?.id || "";

  const captureRun = async (cmd) => {
    try {
      return await $`/bin/sh -lc ${cmd}`.cwd(directory).text();
    } catch (err) {
      await logFailure(cmd, err);
      return "";
    }
  };

  const loadPrime = async (source = "startup", sessionID = "") => {
    const env = [`GT_HOOK_SOURCE=${shellQuote(source)}`];
    if (sessionID) env.push(`GT_SESSION_ID=${shellQuote(sessionID)}`);
    return await captureRun(`${env.join(" ")} ${gtCommand()} prime --hook`);
  };

  // --- Turn-boundary drain / propulsion -------------------------------------
  // Mirror Claude's UserPromptSubmit gate: witness/refinery/deacon/boot do NOT
  // self-drive (they wake on their own signals); everyone else does.
  const selfDrives = !/witness|refinery|deacon|boot/.test(role);
  // Roles that must stay alive across empty-idle (poll until work arrives).
  const longLived = /mayor/.test(role);
  const pollMs = Math.max(5000, parseInt(process.env.GT_OC_POLL_MS || "20000", 10) || 20000);
  let pollTimer = null;
  let draining = false;

  // Drain the queue; if there is queued work, start a new turn with it.
  // Returns true iff a new prompt was injected.
  const drainAndContinue = async (sid) => {
    if (!sid || draining) return false;
    draining = true;
    try {
      const out = await captureRun(`${gtCommand()} mail check --inject`);
      if (out && out.trim()) {
        try {
          await client.session.prompt({
            path: { id: sid },
            body: { parts: [{ type: "text", text: out }] },
          });
          return true;
        } catch (err) {
          await logFailure(`session.prompt(drain sid=${sid})`, err);
        }
      }
      return false;
    } finally {
      draining = false;
    }
  };

  const stopPoll = () => { if (pollTimer) { clearTimeout(pollTimer); pollTimer = null; } };

  // Keep checking while idle; the moment work appears, inject it (which fires a
  // new turn -> session.idle again -> this restarts naturally).
  const startPoll = (sid) => {
    if (pollTimer) return;
    const tick = async () => {
      pollTimer = null;
      const acted = await drainAndContinue(sid);
      if (!acted) pollTimer = setTimeout(tick, pollMs);
    };
    pollTimer = setTimeout(tick, pollMs);
  };

  return {
    event: async ({ event }) => {
      if (event?.type === "session.created") {
        if (!didInit) {
          didInit = true;
          primePromise = loadPrime("startup", eventSessionID(event));
        }
      } else if (event?.type === "session.compacted") {
        primePromise = loadPrime("compact", eventSessionID(event));
      } else if (event?.type === "session.deleted") {
        const sessionID = event.properties?.info?.id;
        if (sessionID) await captureRun(`${gtCommand()} costs record --session ${shellQuote(sessionID)}`);
        stopPoll();
      } else if (event?.type === "session.idle" && selfDrives) {
        const sid = eventSessionID(event);
        stopPoll();
        const acted = await drainAndContinue(sid);
        if (!acted && longLived) startPoll(sid);
      }
    },
    "experimental.chat.system.transform": async (input, output) => {
      if (!primePromise) primePromise = loadPrime("startup");
      const context = await primePromise;
      if (context) output.system.push(context);
      else primePromise = null;
    },
    "experimental.session.compacting": async ({ sessionID }, output) => {
      const roleDisplay = simpleRole(role) || "unknown";
      output.context.push(`
## Gas Town Multi-Agent System

**After Compaction:** Run \`gt prime --hook\` to restore full context.
**Check Hook:** \`gt hook\` - if work present, execute immediately (GUPP).
**Role:** ${roleDisplay}
`);
    },
  };
};
