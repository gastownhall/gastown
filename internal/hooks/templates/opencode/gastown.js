// Gas Town OpenCode plugin: hooks SessionStart/Compaction via events.
// Injects gt prime context into the system prompt via experimental.chat.system.transform.
export const GasTown = async ({ directory }) => {
  const role = (process.env.GT_ROLE || "").toLowerCase();
  const gtBin = process.env.GT_BIN || "gt";
  const commandTimeoutMs = 10_000;

  // OpenCode loads one plugin instance per project, so context loading must be
  // isolated by session even when several sessions share the same server.
  const primePromises = new Map();

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

  const commandLabel = (args) =>
    [gtBin, ...args].map((value) => JSON.stringify(String(value))).join(" ");

  const isDoltBackedCommand = (args) =>
    !(args[0] === "dolt" && args[1] === "status");

  const runGT = async (args, env = {}, timeoutMs = 0) => {
    const options = {
      cmd: [gtBin, ...args],
      cwd: directory,
      env: { ...process.env, ...env },
      stdout: "pipe",
      stderr: "pipe",
    };
    if (timeoutMs > 0) options.timeout = timeoutMs;

    const child = Bun.spawn(options);
    const [stdout, stderr, code] = await Promise.all([
      new Response(child.stdout).text(),
      new Response(child.stderr).text(),
      child.exited,
    ]);
    const timedOut = timeoutMs > 0 && child.signalCode != null;
    if (code !== 0 || timedOut) {
      const err = new Error(
        timedOut
          ? `${commandLabel(args)} timed out after ${timeoutMs}ms`
          : `${commandLabel(args)} exited with code ${code}`
      );
      err.exitCode = code;
      err.stdout = stdout;
      err.stderr = stderr;
      err.timedOut = timedOut;
      throw err;
    }
    return stdout;
  };

  const captureDoltStatus = async () => {
    const args = ["dolt", "status"];
    const statusCmd = commandLabel(args);
    try {
      return await runGT(args, {}, 10_000);
    } catch (err) {
      return [
        `status_command: ${statusCmd}`,
        "status_timeout_ms: 10000",
        `status_error: ${err?.message || err}`,
        `status_stdout_tail: ${outputTail(err?.stdout)}`,
        `status_stderr_tail: ${outputTail(err?.stderr)}`,
      ].join("\n");
    }
  };

  const logFailure = async (args, err) => {
    const cmd = commandLabel(args);
    const code = exitCode(err);
    const message = err?.message || String(err);
    const timeout = err?.timedOut || /timed?\s*out/i.test(message)
      ? "yes"
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
    if (isDoltBackedCommand(args)) {
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

  const eventSessionID = (event) =>
    event?.properties?.sessionID || event?.properties?.info?.id || event?.sessionID || event?.session?.id || "";

  const captureRun = async (args, env = {}) => {
    try {
      return await runGT(args, env, commandTimeoutMs);
    } catch (err) {
      await logFailure(args, err);
      return "";
    }
  };

  const loadPrime = async (source = "startup", sessionID = "") => {
    const env = { GT_HOOK_SOURCE: source };
    if (sessionID) {
      env.GT_SESSION_ID = sessionID;
    }
    const context = await captureRun(["prime", "--hook"], env);
    // NOTE: session-started nudge to deacon removed — it interrupted
    // the deacon's await-signal backoff. Deacon wakes on beads activity.
    return context;
  };

  const setPrime = (source, sessionID) => {
    const promise = loadPrime(source, sessionID);
    primePromises.set(sessionID, promise);
    return promise;
  };

  // Do not call `gt costs record` on session.deleted: it reads Claude
  // transcripts and OpenCode session IDs are not tmux session names.
  return {
    event: async ({ event }) => {
      if (event?.type === "session.created") {
        const sessionID = eventSessionID(event);
        if (!primePromises.has(sessionID)) {
          // Start loading prime context early; system.transform will await it.
          setPrime("startup", sessionID);
        }
      }
      if (event?.type === "session.compacted") {
        // Reset so next system.transform gets fresh context.
        const sessionID = eventSessionID(event);
        setPrime("compact", sessionID);
      }
      if (event?.type === "session.deleted") {
        primePromises.delete(eventSessionID(event));
      }
    },
    "experimental.chat.system.transform": async (input, output) => {
      const sessionID = input?.sessionID || "";
      // If session.created hasn't fired yet, start loading now.
      let primePromise = primePromises.get(sessionID);
      if (!primePromise) {
        primePromise = setPrime("startup", sessionID);
      }
      const context = await primePromise;
      if (context) {
        output.system.push(context);
      } else {
        // Reset so next transform retries instead of pushing empty forever.
        if (primePromises.get(sessionID) === primePromise) {
          primePromises.delete(sessionID);
        }
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
