// Gas Town Pi Extension — Enhanced (with per-prompt mail check)
// Deploys the same lifecycle hooks as Claude's settings-autonomous.json
// but using pi's extension API.
//
// Events mapped:
//   session_start       → gt prime --hook (capture context)
//   before_agent_start  → inject captured context + check mail every prompt
//   tool_call           → gt tap guard pr-workflow (on git push/pr create)
//   session_shutdown    → gt costs record
//
// Enhancement over upstream: mail is checked on every prompt (throttled to
// 30s) via before_agent_start, matching Claude's UserPromptSubmit behavior.
//
// Loaded via: pi -e gastown-hooks.js

export default (pi) => {
  const role = (process.env.GT_ROLE || "").toLowerCase();
  let primeContext = null;
  let contextInjected = false;
  let lastMailCheck = 0;

  // Pi exposes session metadata to model-run bash tools, but pi.exec inherits
  // only the parent process environment. Forward the current session ID using
  // Gas Town's unconditional hook variable so direct Pi launches work even
  // when no wrapper supplied GT_AGENT or GT_SESSION_ID_ENV.
  const runGT = async (context, args) => {
    const previousSessionId = process.env.GT_SESSION_ID;
    process.env.GT_SESSION_ID = context.sessionManager.getSessionId();
    try {
      return await pi.exec("{{GT_BIN}}", args);
    } finally {
      if (previousSessionId === undefined) {
        delete process.env.GT_SESSION_ID;
      } else {
        process.env.GT_SESSION_ID = previousSessionId;
      }
    }
  };

  const shouldCheckMail = () =>
    !role.includes("witness") && !role.includes("refinery") && !role.startsWith("deacon") && !role.includes("boot");

  // SessionStart — run gt prime and capture context for injection
  pi.on("session_start", async (event, context) => {
    try {
      const result = await runGT(context, ["prime", "--hook"]);
      if (result.code === 0 && result.stdout.trim()) {
        primeContext = result.stdout.trim();
        console.error("[gastown] gt prime captured (" + primeContext.length + " chars)");
      } else {
        console.error("[gastown] gt prime returned no output (code=" + result.code + ")");
      }
    } catch (e) {
      console.error("[gastown] gt prime failed:", e.message);
    }

  });

  // BeforeAgentStart — inject prime context + check mail every prompt
  pi.on("before_agent_start", async (event, context) => {
    let mailContext = null;

    // Check mail on every prompt (throttled to once per 30s)
    const now = Date.now();
    if (shouldCheckMail() && now - lastMailCheck >= 30000) {
      lastMailCheck = now;
      try {
        const mailResult = await runGT(context, ["mail", "check", "--inject"]);
        if (mailResult.code === 0 && mailResult.stdout.trim()) {
          mailContext = mailResult.stdout.trim();
          console.error("[gastown] mail check: new mail found");
        }
      } catch (e) {
        console.error("[gastown] per-prompt mail check failed:", e.message);
      }
    }

    // Inject prime context on first prompt
    if (primeContext && !contextInjected) {
      contextInjected = true;
      console.error("[gastown] injecting prime context into session");
      const result = {
        message: {
          customType: "gastown-prime",
          content: primeContext,
          display: false,
        },
        systemPrompt: event.systemPrompt + "\n\n" + primeContext,
      };
      if (mailContext) {
        result.systemPrompt += "\n\n" + mailContext;
        result.message.content += "\n\n" + mailContext;
      }
      return result;
    }

    // After first prompt, inject mail if present
    if (mailContext) {
      return {
        message: {
          customType: "gastown-mail",
          content: mailContext,
          display: false,
        },
        systemPrompt: event.systemPrompt + "\n\n" + mailContext,
      };
    }
  });

  // PreToolUse equivalent — guard dangerous git operations
  pi.on("tool_call", async (event, context) => {
    if (event.toolName === "bash" && event.input?.command) {
      const cmd = event.input.command;
      if (
        cmd.includes("git push") ||
        cmd.includes("gh pr create") ||
        cmd.includes("git checkout -b")
      ) {
        try {
          const result = await runGT(context, ["tap", "guard", "pr-workflow"]);
          if (result.code !== 0) {
            return { block: true, reason: result.stderr || "gt tap guard rejected this operation" };
          }
        } catch (e) {
          console.error("[gastown] gt tap guard failed:", e.message);
        }
      }
    }
  });

  // Stop equivalent — record API costs
  pi.on("session_shutdown", async (event, context) => {
    try {
      await runGT(context, ["costs", "record"]);
    } catch (e) {
      console.error("[gastown] gt costs record failed:", e.message);
    }
  });
};
