// Gas Town OpenCode plugin: hooks SessionStart/Compaction via events.
// Injects gt prime context into the system prompt via experimental.chat.system.transform.
export const GasTown = async ({ $, directory }) => {
  const role = (process.env.GT_ROLE || "").toLowerCase();
  const gtBin = process.env.GT_BIN || "gt";
  let didInit = false;

  // Promise-based context loading ensures the system transform hook can
  // await the result even if session.created hasn't resolved yet.
  let primePromise = null;

  const outputTail = (value, maxChars = 2000) => {
    if (value === undefined || value === null) return "<empty>";
    let text;
    if (typeof value === "string") {
      text = value;
    } else if (value instanceof Uint8Array) {
      text = new TextDecoder().decode(value);
    } else {
      text = String(value);
    }
    text = text.trimEnd();
    if (!text) return "<empty>";
    if (text.length <= maxChars) return text;
    return `...<truncated ${text.length - maxChars} chars>\n${text.slice(-maxChars)}`;
  };

  const exitCode = (err) => {
    const candidates = [err?.exitCode, err?.exit_code, err?.status, err?.code];
    for (const code of candidates) {
      if (typeof code === "number") return code;
    }
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
      ? "yes (exit code 124 / timeout)"
      : "not indicated";
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
      lines.push(
        "suggested_recovery: If Dolt is unhealthy or another gt/bd command is hanging, capture SIGQUIT and `gt dolt status` diagnostics before escalating; otherwise retry after the timeout clears."
      );
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

  const eventSessionID = (event) => event?.properties?.info?.id || event?.sessionID || event?.session?.id || "";

  const captureRun = async (cmd) => {
    try {
      // .text() captures stdout as a string and suppresses terminal echo.
      return await $`/bin/sh -lc ${cmd}`.cwd(directory).text();
    } catch (err) {
      await logFailure(cmd, err);
      return "";
    }
  };

  const loadPrime = async (source = "startup", sessionID = "") => {
    const env = [`GT_HOOK_SOURCE=${shellQuote(source)}`];
    if (sessionID) {
      env.push(`GT_SESSION_ID=${shellQuote(sessionID)}`);
    }
    let context = await captureRun(`${env.join(" ")} ${gtCommand()} prime --hook`);
    // NOTE: session-started nudge to deacon removed — it interrupted
    // the deacon's await-signal backoff. Deacon wakes on beads activity.
    return context;
  };

  return {
    event: async ({ event }) => {
      if (event?.type === "session.created") {
        if (didInit) return;
        didInit = true;
        // Start loading prime context early; system.transform will await it.
        primePromise = loadPrime("startup", eventSessionID(event));
      }
      if (event?.type === "session.compacted") {
        // Reset so next system.transform gets fresh context.
        primePromise = loadPrime("compact", eventSessionID(event));
      }
      if (event?.type === "session.deleted") {
        const sessionID = event.properties?.info?.id;
        if (sessionID) {
          await captureRun(`${gtCommand()} costs record --session ${shellQuote(sessionID)}`);
        }
      }
    },
    "experimental.chat.system.transform": async (input, output) => {
      // If session.created hasn't fired yet, start loading now.
      if (!primePromise) {
        primePromise = loadPrime("startup");
      }
      const context = await primePromise;
      if (context) {
        output.system.push(context);
      } else {
        // Reset so next transform retries instead of pushing empty forever.
        primePromise = null;
      }
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
