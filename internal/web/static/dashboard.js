(function() {
    'use strict';

    // ============================================
    // CSRF PROTECTION
    // ============================================
    // Inject dashboard token into all POST requests to prevent cross-site request forgery.
    var _origFetch = window.fetch;
    var _csrfMeta = document.querySelector('meta[name="dashboard-token"]');
    var _csrfToken = _csrfMeta ? _csrfMeta.getAttribute('content') : '';
    window.fetch = function(url, opts) {
        opts = opts || {};
        if (opts.method && opts.method.toUpperCase() === 'POST' && _csrfToken) {
            opts.headers = opts.headers || {};
            opts.headers['X-Dashboard-Token'] = _csrfToken;
        }
        return _origFetch.call(this, url, opts);
    };

    // ============================================
    // SSE (Server-Sent Events) CONNECTION
    // ============================================
    window.sseConnected = false;
    var evtSource = null;
    var sseReconnectDelay = 1000;
    var sseMaxReconnectDelay = 30000;

    function connectSSE() {
        if (evtSource) {
            evtSource.close();
        }

        evtSource = new EventSource('/api/events');

        evtSource.addEventListener('connected', function() {
            window.sseConnected = true;
            sseReconnectDelay = 1000;
            updateConnectionStatus('live');
        });

        evtSource.addEventListener('dashboard-update', function(e) {
            if (window.pauseRefresh) return;
            // Trigger HTMX to re-fetch the dashboard
            var dashboard = document.getElementById('dashboard-main');
            if (dashboard && typeof htmx !== 'undefined') {
                htmx.trigger(dashboard, 'sse:dashboard-update');
            }
        });

        evtSource.onerror = function() {
            window.sseConnected = false;
            updateConnectionStatus('reconnecting');
            evtSource.close();
            // Exponential backoff reconnect
            setTimeout(function() {
                sseReconnectDelay = Math.min(sseReconnectDelay * 2, sseMaxReconnectDelay);
                connectSSE();
            }, sseReconnectDelay);
        };
    }

    function updateConnectionStatus(state) {
        var el = document.getElementById('connection-status');
        if (!el) return;
        switch (state) {
            case 'live':
                el.textContent = 'Live';
                el.className = 'connection-live';
                break;
            case 'reconnecting':
                el.textContent = 'Reconnecting...';
                el.className = 'connection-reconnecting';
                break;
            default:
                el.textContent = 'Connecting...';
                el.className = '';
        }
    }

    // Start SSE connection
    connectSSE();

    // ============================================
    // EXPAND BUTTON HANDLER
    // ============================================
    document.addEventListener('click', function(e) {
        var btn = e.target.closest('.expand-btn');
        if (!btn) return;

        e.preventDefault();
        var panel = btn.closest('.panel');
        if (!panel) return;

        if (panel.classList.contains('expanded')) {
            panel.classList.remove('expanded');
            btn.textContent = 'Expand';
            // Resume refresh when panel is collapsed
            window.pauseRefresh = false;
        } else {
            document.querySelectorAll('.panel.expanded').forEach(function(p) {
                p.classList.remove('expanded');
                var b = p.querySelector('.expand-btn');
                if (b) b.textContent = 'Expand';
            });
            panel.classList.add('expanded');
            btn.textContent = '✕ Close';
            // Pause refresh while panel is expanded
            window.pauseRefresh = true;
        }
    });

    // ============================================
    // COLLAPSE BUTTON HANDLER
    // ============================================
    document.addEventListener('click', function(e) {
        var btn = e.target.closest('.collapse-btn');
        if (!btn) return;

        e.preventDefault();
        var panel = btn.closest('.panel');
        if (!panel) return;

        panel.classList.toggle('collapsed');
    });

    // After HTMX swap - morph preserves most state, but we need to re-init some things
    document.body.addEventListener('htmx:afterSwap', function() {
        // Morph preserves expanded class, so we don't need to close panels anymore
        // Just check if we should resume refresh
        var hasExpanded = document.querySelector('.panel.expanded');
        var mailDetail = document.getElementById('mail-detail');
        var mailCompose = document.getElementById('mail-compose');
        var issueDetail = document.getElementById('issue-detail');
        var prDetail = document.getElementById('pr-detail');
        var convoyDetailView = document.getElementById('convoy-detail');
        var convoyCreateView = document.getElementById('convoy-create-form');
        var sessionPreview = document.getElementById('session-preview');
        var inDetailView = (mailDetail && mailDetail.style.display !== 'none') ||
                          (mailCompose && mailCompose.style.display !== 'none') ||
                          (issueDetail && issueDetail.style.display !== 'none') ||
                          (prDetail && prDetail.style.display !== 'none') ||
                          (convoyDetailView && convoyDetailView.style.display !== 'none') ||
                          (convoyCreateView && convoyCreateView.style.display !== 'none') ||
                          (sessionPreview && sessionPreview.style.display !== 'none');
        if (!inDetailView && !hasExpanded) {
            window.pauseRefresh = false;
        }
        // Reload dynamic panels after swap (handled via window functions)
        if (window.refreshCrewPanel) window.refreshCrewPanel();
        if (window.refreshReadyPanel) window.refreshReadyPanel();
        // Update connection status indicator after morph
        updateConnectionStatus(window.sseConnected ? 'live' : 'reconnecting');
    });

    // ============================================
    // COMMAND PALETTE
    // ============================================
    var allCommands = [];
    var visibleCommands = [];
    var selectedIdx = 0;
    var isPaletteOpen = false;
    var executionLock = false;
    var pendingCommand = null; // Command waiting for args
    var cachedOptions = null;  // Cached options from /api/options
    var recentCommands = [];   // Recently executed commands (from localStorage)
    var MAX_RECENT = 10;
    var RECENT_STORAGE_KEY = 'gt-palette-recent';

    // Load recent commands from localStorage
    function loadRecentCommands() {
        try {
            var stored = localStorage.getItem(RECENT_STORAGE_KEY);
            if (stored) {
                recentCommands = JSON.parse(stored);
                if (!Array.isArray(recentCommands)) recentCommands = [];
                // Cap at MAX_RECENT
                recentCommands = recentCommands.slice(0, MAX_RECENT);
            }
        } catch (e) {
            recentCommands = [];
        }
    }

    // Save a command to recent history
    function saveRecentCommand(cmdName) {
        // Remove duplicate if exists
        recentCommands = recentCommands.filter(function(c) { return c !== cmdName; });
        // Add to front
        recentCommands.unshift(cmdName);
        // Cap at MAX_RECENT
        recentCommands = recentCommands.slice(0, MAX_RECENT);
        try {
            localStorage.setItem(RECENT_STORAGE_KEY, JSON.stringify(recentCommands));
        } catch (e) {
            // localStorage full or unavailable, ignore
        }
    }

    // Detect active context based on expanded panel or visible detail view
    function detectActiveContext() {
        var expandedPanel = document.querySelector('.panel.expanded');
        if (expandedPanel) {
            var panelId = expandedPanel.id || '';
            if (panelId.indexOf('mail') !== -1) return 'Mail';
            if (panelId.indexOf('crew') !== -1) return 'Crew';
            if (panelId.indexOf('issue') !== -1 || panelId.indexOf('work') !== -1) return 'Work';
            if (panelId.indexOf('ready') !== -1) return 'Work';
            if (panelId.indexOf('pr') !== -1 || panelId.indexOf('merge') !== -1) return 'Status';
        }
        // Check detail views
        var mailDetail = document.getElementById('mail-detail');
        var mailCompose = document.getElementById('mail-compose');
        if ((mailDetail && mailDetail.style.display !== 'none') ||
            (mailCompose && mailCompose.style.display !== 'none')) return 'Mail';
        var issueDetail = document.getElementById('issue-detail');
        if (issueDetail && issueDetail.style.display !== 'none') return 'Work';
        var prDetail = document.getElementById('pr-detail');
        if (prDetail && prDetail.style.display !== 'none') return 'Status';
        return null;
    }

    // Score a command for fuzzy matching. Returns -1 for no match, higher is better.
    function scoreCommand(cmd, query) {
        var name = cmd.name.toLowerCase();
        var desc = cmd.desc.toLowerCase();
        var cat = cmd.category.toLowerCase();
        var q = query.toLowerCase();

        // Exact prefix match on name is best
        if (name.indexOf(q) === 0) return 100 + (50 - name.length);
        // Prefix match on a word within the name
        var nameParts = name.split(' ');
        for (var i = 0; i < nameParts.length; i++) {
            if (nameParts[i].indexOf(q) === 0) return 80 + (50 - name.length);
        }
        // Substring match in name
        if (name.indexOf(q) !== -1) return 60 + (50 - name.length);
        // Match in description
        if (desc.indexOf(q) !== -1) return 40;
        // Match in category
        if (cat.indexOf(q) !== -1) return 20;
        // Fuzzy: all query chars appear in order in name
        var ni = 0;
        for (var qi = 0; qi < q.length; qi++) {
            ni = name.indexOf(q[qi], ni);
            if (ni === -1) return -1;
            ni++;
        }
        return 10;
    }

    // Highlight matching portions in text for display
    function highlightMatch(text, query) {
        if (!query) return escapeHtml(text);
        var lowerText = text.toLowerCase();
        var lowerQuery = query.toLowerCase();
        var idx = lowerText.indexOf(lowerQuery);
        if (idx !== -1) {
            return escapeHtml(text.substring(0, idx)) +
                '<mark>' + escapeHtml(text.substring(idx, idx + query.length)) + '</mark>' +
                escapeHtml(text.substring(idx + query.length));
        }
        return escapeHtml(text);
    }

    loadRecentCommands();

    var overlay = document.getElementById('command-palette-overlay');
    var searchInput = document.getElementById('command-palette-input');
    var resultsDiv = document.getElementById('command-palette-results');
    var toastContainer = document.getElementById('toast-container');
    var outputPanel = document.getElementById('output-panel');
    var outputContent = document.getElementById('output-panel-content');
    var outputCmd = document.getElementById('output-panel-cmd');

    // Output panel
    function showOutput(cmd, output) {
        outputCmd.textContent = 'gt ' + cmd;
        outputContent.textContent = output;
        outputPanel.classList.add('open');
    }

    document.getElementById('output-close-btn').onclick = function() {
        outputPanel.classList.remove('open');
    };

    document.getElementById('output-copy-btn').onclick = function() {
        navigator.clipboard.writeText(outputContent.textContent).then(function() {
            showToast('success', 'Copied', 'Output copied to clipboard');
        });
    };

    // Load commands once
    fetch('/api/commands')
        .then(function(r) { return r.json(); })
        .then(function(data) {
            allCommands = data.commands || [];
        })
        .catch(function() {
            console.error('Failed to load commands');
        });

    // Fetch dynamic options (rigs, polecats, convoys, agents, hooks)
    function fetchOptions() {
        return fetch('/api/options')
            .then(function(r) { return r.json(); })
            .then(function(data) {
                cachedOptions = data;
                return data;
            })
            .catch(function() {
                console.error('Failed to load options');
                return null;
            });
    }

    // Get options for a specific argType
    // Returns array of {value, label, disabled} objects
    function getOptionsForType(argType) {
        if (!cachedOptions) return [];

        var rawOptions;
        switch (argType) {
            case 'rigs': rawOptions = cachedOptions.rigs || []; break;
            case 'polecats': rawOptions = cachedOptions.polecats || []; break;
            case 'convoys': rawOptions = cachedOptions.convoys || []; break;
            case 'agents': rawOptions = cachedOptions.agents || []; break;
            case 'hooks': rawOptions = cachedOptions.hooks || []; break;
            case 'messages': rawOptions = cachedOptions.messages || []; break;
            case 'crew': rawOptions = cachedOptions.crew || []; break;
            case 'escalations': rawOptions = cachedOptions.escalations || []; break;
            default: return [];
        }

        // Normalize to {value, label, disabled} format
        return rawOptions.map(function(opt) {
            if (typeof opt === 'string') {
                return { value: opt, label: opt, disabled: false };
            }
            // Agent format: {name, status, running}
            var statusText = opt.running ? '● running' : '○ stopped';
            return {
                value: opt.name,
                label: opt.name + ' (' + statusText + ')',
                disabled: !opt.running,
                running: opt.running
            };
        });
    }

    function escapeHtml(str) {
        if (!str) return '';
        var div = document.createElement('div');
        div.textContent = str;
        return div.innerHTML;
    }

    // Parse args template like "<address> -s <subject> -m <message>" into field definitions
    // Returns [{name: "address", flag: null}, {name: "subject", flag: "-s"}, {name: "message", flag: "-m"}]
    function parseArgsTemplate(argsStr) {
        if (!argsStr) return [];
        var args = [];
        // Match patterns like "<name>" or "-f <name>"
        var regex = /(?:(-\w+)\s+)?<([^>]+)>/g;
        var match;
        while ((match = regex.exec(argsStr)) !== null) {
            args.push({ name: match[2], flag: match[1] || null });
        }
        return args;
    }

    function renderResults() {
        // If waiting for args, show the args input with options
        if (pendingCommand) {
            var options = pendingCommand.argType ? getOptionsForType(pendingCommand.argType) : [];
            var argFields = parseArgsTemplate(pendingCommand.args);

            var formHtml = '<div class="command-args-prompt">' +
                '<div class="command-args-header">gt ' + escapeHtml(pendingCommand.name) + '</div>';

            // Build form fields for each argument
            for (var i = 0; i < argFields.length; i++) {
                var field = argFields[i];
                var fieldId = 'arg-field-' + i;
                var isFirstField = (i === 0) && !field.flag; // First positional arg
                var hasOptions = isFirstField && pendingCommand.argType && options.length > 0;
                var noOptions = isFirstField && pendingCommand.argType && options.length === 0;
                var isMessageField = field.name === 'message' || field.name === 'body';

                formHtml += '<div class="command-field">';
                formHtml += '<label class="command-field-label" for="' + fieldId + '">' + escapeHtml(field.name) + '</label>';

                if (hasOptions) {
                    // Dropdown for first arg when options exist
                    formHtml += '<select id="' + fieldId + '" class="command-field-select" data-flag="' + (field.flag || '') + '">';
                    formHtml += '<option value="">Select ' + escapeHtml(field.name) + '...</option>';
                    for (var j = 0; j < options.length; j++) {
                        var opt = options[j];
                        var disabledAttr = opt.disabled ? ' disabled' : '';
                        var optClass = opt.disabled ? ' class="option-disabled"' : (opt.running ? ' class="option-running"' : '');
                        formHtml += '<option value="' + escapeHtml(opt.value) + '"' + disabledAttr + optClass + '>' + escapeHtml(opt.label) + '</option>';
                    }
                    formHtml += '</select>';
                } else if (noOptions) {
                    formHtml += '<input type="text" id="' + fieldId + '" class="command-field-input" data-flag="' + (field.flag || '') + '" placeholder="No ' + escapeHtml(pendingCommand.argType) + ' available">';
                } else if (isMessageField) {
                    formHtml += '<textarea id="' + fieldId + '" class="command-field-textarea" data-flag="' + (field.flag || '') + '" placeholder="Enter ' + escapeHtml(field.name) + '..." rows="3"></textarea>';
                } else {
                    formHtml += '<input type="text" id="' + fieldId + '" class="command-field-input" data-flag="' + (field.flag || '') + '" placeholder="Enter ' + escapeHtml(field.name) + '...">';
                }
                formHtml += '</div>';
            }

            // If no arg fields parsed, show generic input
            if (argFields.length === 0 && pendingCommand.args) {
                formHtml += '<div class="command-field">';
                formHtml += '<input type="text" id="arg-field-0" class="command-field-input" placeholder="' + escapeHtml(pendingCommand.args) + '">';
                formHtml += '</div>';
            }

            formHtml += '<div class="command-args-actions">' +
                '<button id="command-args-run" class="command-args-btn run">Run</button>' +
                '<button id="command-args-cancel" class="command-args-btn cancel">Cancel</button>' +
                '</div></div>';

            resultsDiv.innerHTML = formHtml;

            // Focus first field
            var firstField = resultsDiv.querySelector('#arg-field-0');
            if (firstField) firstField.focus();

            // Wire up run/cancel buttons
            var runBtn = document.getElementById('command-args-run');
            var cancelBtn = document.getElementById('command-args-cancel');

            if (runBtn) {
                runBtn.onclick = function() {
                    runBtn.classList.add('loading');
                    runBtn.textContent = 'Running';
                    runWithArgsFromForm(argFields.length || 1);
                };
            }
            if (cancelBtn) {
                cancelBtn.onclick = cancelArgs;
            }

            // Enter key submits
            resultsDiv.querySelectorAll('input, select').forEach(function(el) {
                el.onkeydown = function(e) {
                    if (e.key === 'Enter') {
                        e.preventDefault();
                        runWithArgsFromForm(argFields.length || 1);
                    } else if (e.key === 'Escape') {
                        e.preventDefault();
                        cancelArgs();
                    }
                };
            });
            return;
        }

        if (visibleCommands.length === 0) {
            resultsDiv.innerHTML = '<div class="command-palette-empty">No matching commands</div>';
            return;
        }
        var currentQuery = searchInput ? searchInput.value.trim() : '';
        var html = '';

        if (currentQuery) {
            // Search mode: flat list with highlights
            for (var i = 0; i < visibleCommands.length; i++) {
                var cmd = visibleCommands[i];
                var cls = 'command-item' + (i === selectedIdx ? ' selected' : '');
                var argsHint = cmd.args ? ' <span class="command-args">' + escapeHtml(cmd.args) + '</span>' : '';
                var nameHtml = highlightMatch('gt ' + cmd.name, currentQuery);
                html += '<div class="' + cls + '" data-cmd-name="' + escapeHtml(cmd.name) + '" data-cmd-args="' + escapeHtml(cmd.args || '') + '">' +
                    '<span class="command-name">' + nameHtml + argsHint + '</span>' +
                    '<span class="command-desc">' + escapeHtml(cmd.desc) + '</span>' +
                    '<span class="command-category">' + escapeHtml(cmd.category) + '</span>' +
                    '</div>';
            }
        } else {
            // Browse mode: show Recent, Contextual, then All Commands
            // visibleCommands was rebuilt by filterCommands with sections baked in
            for (var j = 0; j < visibleCommands.length; j++) {
                var item = visibleCommands[j];
                if (item._section) {
                    // Section header
                    html += '<div class="command-section-header">' + escapeHtml(item._section) + '</div>';
                    continue;
                }
                var cls2 = 'command-item' + (j === selectedIdx ? ' selected' : '');
                var argsHint2 = item.args ? ' <span class="command-args">' + escapeHtml(item.args) + '</span>' : '';
                var icon = item._recent ? '<span class="command-recent-icon">&#8635;</span>' : '';
                html += '<div class="' + cls2 + '" data-cmd-name="' + escapeHtml(item.name) + '" data-cmd-args="' + escapeHtml(item.args || '') + '">' +
                    icon +
                    '<span class="command-name">gt ' + escapeHtml(item.name) + argsHint2 + '</span>' +
                    '<span class="command-desc">' + escapeHtml(item.desc) + '</span>' +
                    '<span class="command-category">' + escapeHtml(item.category) + '</span>' +
                    '</div>';
            }
        }
        resultsDiv.innerHTML = html;

        // Scroll selected item into view
        var selectedEl = resultsDiv.querySelector('.command-item.selected');
        if (selectedEl) {
            selectedEl.scrollIntoView({ block: 'nearest' });
        }
    }

    function runWithArgsFromForm(fieldCount) {
        var args = [];
        for (var i = 0; i < fieldCount; i++) {
            var field = document.getElementById('arg-field-' + i);
            if (field) {
                var val = field.value.trim();
                var flag = field.getAttribute('data-flag');
                if (val) {
                    if (flag) {
                        // Flag-based arg: -s "value"
                        args.push(flag);
                        args.push('"' + val.replace(/"/g, '\\"') + '"');
                    } else {
                        // Positional arg
                        args.push(val);
                    }
                }
            }
        }
        if (pendingCommand) {
            var fullCmd = pendingCommand.name + (args.length ? ' ' + args.join(' ') : '');
            pendingCommand = null;
            runCommand(fullCmd);
        }
    }

    function runWithArgs() {
        runWithArgsFromForm(10); // fallback
    }

    function cancelArgs() {
        pendingCommand = null;
        filterCommands(searchInput ? searchInput.value : '');
    }

    function filterCommands(query) {
        query = (query || '').trim();
        if (!query) {
            // Build sectioned list: Recent, Contextual, All Commands
            visibleCommands = [];
            var shownNames = {};

            // Recent section
            var recentItems = [];
            for (var ri = 0; ri < recentCommands.length; ri++) {
                var recentCmd = allCommands.find(function(c) { return c.name === recentCommands[ri]; });
                if (recentCmd) recentItems.push(recentCmd);
            }
            if (recentItems.length > 0) {
                visibleCommands.push({ _section: 'Recent' });
                for (var ri2 = 0; ri2 < recentItems.length; ri2++) {
                    var rcmd = Object.assign({}, recentItems[ri2], { _recent: true });
                    visibleCommands.push(rcmd);
                    shownNames[rcmd.name] = true;
                }
            }

            // Contextual section
            var context = detectActiveContext();
            if (context) {
                var contextItems = allCommands.filter(function(c) {
                    return c.category === context && !shownNames[c.name];
                });
                if (contextItems.length > 0) {
                    visibleCommands.push({ _section: 'Suggested \u2014 ' + context });
                    for (var ci = 0; ci < contextItems.length; ci++) {
                        visibleCommands.push(contextItems[ci]);
                        shownNames[contextItems[ci].name] = true;
                    }
                }
            }

            // All commands section (remaining)
            var remaining = allCommands.filter(function(c) { return !shownNames[c.name]; });
            remaining.sort(function(a, b) { return a.name.localeCompare(b.name); });
            if (remaining.length > 0) {
                visibleCommands.push({ _section: 'All Commands' });
                for (var ai = 0; ai < remaining.length; ai++) {
                    visibleCommands.push(remaining[ai]);
                }
            }
        } else {
            // Score and sort by relevance
            var scored = [];
            for (var i = 0; i < allCommands.length; i++) {
                var s = scoreCommand(allCommands[i], query);
                if (s > 0) {
                    scored.push({ cmd: allCommands[i], score: s });
                }
            }
            scored.sort(function(a, b) { return b.score - a.score; });
            visibleCommands = scored.map(function(item) { return item.cmd; });
        }
        selectedIdx = 0;
        // In browse mode, skip section headers for initial selection
        while (selectedIdx < visibleCommands.length && visibleCommands[selectedIdx]._section) {
            selectedIdx++;
        }
        renderResults();
    }

    function openPalette() {
        isPaletteOpen = true;
        pendingCommand = null;
        if (overlay) {
            overlay.style.display = 'flex';
            overlay.classList.add('open');
        }
        if (searchInput) {
            searchInput.value = '';
            searchInput.focus();
        }
        filterCommands('');
        // Fetch fresh options in background
        fetchOptions();
    }

    function closePalette() {
        isPaletteOpen = false;
        pendingCommand = null;
        if (overlay) {
            overlay.classList.remove('open');
            overlay.style.display = 'none';
        }
        if (searchInput) {
            searchInput.value = '';
        }
        visibleCommands = [];
        if (resultsDiv) {
            resultsDiv.innerHTML = '';
        }
    }

    function selectCommand(cmdName, cmdArgs) {
        // If command needs args, show args input
        if (cmdArgs) {
            var cmd = allCommands.find(function(c) { return c.name === cmdName; });
            if (cmd) {
                pendingCommand = cmd;
                // Make sure options are loaded before rendering
                if (cmd.argType && !cachedOptions) {
                    fetchOptions().then(function() {
                        renderResults();
                    });
                } else {
                    renderResults();
                }
                return;
            }
        }
        // No args needed, run directly
        runCommand(cmdName);
    }

    function runCommand(cmdName) {
        if (executionLock) {
            console.log('Execution locked, ignoring');
            return;
        }
        if (!cmdName) {
            console.log('No command name');
            return;
        }

        // Close palette FIRST before anything else
        closePalette();

        // Save to recent commands history
        // Extract base command name (without args) for history
        var baseName = cmdName.split(' ').slice(0, 3).join(' ');
        var matchedCmd = allCommands.find(function(c) { return cmdName.indexOf(c.name) === 0; });
        saveRecentCommand(matchedCmd ? matchedCmd.name : baseName);

        executionLock = true;
        console.log('Running command:', cmdName);

        showToast('info', 'Running...', 'gt ' + cmdName);

        var payload = { command: cmdName };
        // Include confirmed flag if the command requires server-side confirmation
        if (matchedCmd && matchedCmd.confirm) {
            payload.confirmed = true;
        }

        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Success', 'gt ' + cmdName);
                if (data.output && data.output.trim()) {
                    showOutput(cmdName, data.output);
                }
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
                if (data.output) {
                    showOutput(cmdName, data.output);
                }
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message || 'Request failed');
        })
        .finally(function() {
            // Unlock after 1 second to prevent double-clicks
            setTimeout(function() {
                executionLock = false;
            }, 1000);
        });
    }

    function showToast(type, title, message) {
        var toast = document.createElement('div');
        toast.className = 'toast ' + type;
        var icon = type === 'success' ? '✓' : type === 'error' ? '✕' : 'ℹ';
        toast.innerHTML = '<span class="toast-icon">' + icon + '</span>' +
            '<div class="toast-content">' +
            '<div class="toast-title">' + escapeHtml(title) + '</div>' +
            '<div class="toast-message">' + escapeHtml(message) + '</div>' +
            '</div>' +
            '<button class="toast-close">✕</button>';
        toastContainer.appendChild(toast);

        setTimeout(function() {
            if (toast.parentNode) toast.parentNode.removeChild(toast);
        }, 4000);

        toast.querySelector('.toast-close').onclick = function() {
            if (toast.parentNode) toast.parentNode.removeChild(toast);
        };
    }

    // SINGLE click handler for command palette
    resultsDiv.addEventListener('click', function(e) {
        var item = e.target.closest('.command-item');
        if (!item) return;

        e.preventDefault();
        e.stopPropagation();

        var cmdName = item.getAttribute('data-cmd-name');
        var cmdArgs = item.getAttribute('data-cmd-args');
        if (cmdName) {
            selectCommand(cmdName, cmdArgs);
        }
    });

    // Open palette button
    document.addEventListener('click', function(e) {
        if (e.target.closest('#open-palette-btn')) {
            e.preventDefault();
            openPalette();
            return;
        }
        // Click on overlay background closes palette
        if (e.target === overlay) {
            closePalette();
        }
    });

    // Keyboard handling
    document.addEventListener('keydown', function(e) {
        // Cmd+K or Ctrl+K toggles palette
        if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
            e.preventDefault();
            if (isPaletteOpen) {
                closePalette();
            } else {
                openPalette();
            }
            return;
        }

        // Escape closes expanded panels when palette is not open
        if (!isPaletteOpen && e.key === 'Escape') {
            var expanded = document.querySelector('.panel.expanded');
            if (expanded) {
                e.preventDefault();
                expanded.classList.remove('expanded');
                var expandBtn = expanded.querySelector('.expand-btn');
                if (expandBtn) expandBtn.textContent = 'Expand';
                window.pauseRefresh = false;
                return;
            }
        }

        // Rest only when palette is open
        if (!isPaletteOpen) return;

        // If in args mode, let the args input handle keys
        if (pendingCommand) return;

        if (e.key === 'Escape') {
            e.preventDefault();
            closePalette();
            return;
        }

        if (e.key === 'ArrowDown') {
            e.preventDefault();
            if (visibleCommands.length > 0) {
                var next = selectedIdx + 1;
                // Skip section headers
                while (next < visibleCommands.length && visibleCommands[next]._section) next++;
                if (next < visibleCommands.length) selectedIdx = next;
                renderResults();
            }
            return;
        }

        if (e.key === 'ArrowUp') {
            e.preventDefault();
            var prev = selectedIdx - 1;
            // Skip section headers
            while (prev >= 0 && visibleCommands[prev]._section) prev--;
            if (prev >= 0) selectedIdx = prev;
            renderResults();
            return;
        }

        if (e.key === 'Enter') {
            e.preventDefault();
            var selected = visibleCommands[selectedIdx];
            if (selected && !selected._section) {
                selectCommand(selected.name, selected.args);
            }
            return;
        }
    });

    // Input filtering
    searchInput.addEventListener('input', function() {
        filterCommands(searchInput.value);
    });

    // ============================================
    // MAIL PANEL INTERACTIONS
    // ============================================
    var mailList = document.getElementById('mail-list');
    var mailAll = document.getElementById('mail-all');
    var mailDetail = document.getElementById('mail-detail');
    var mailCompose = document.getElementById('mail-compose');
    var currentMessageId = null;
    var currentMessageFrom = null;
    var currentMailTab = 'inbox';

    // Mail tab switching
    document.querySelectorAll('.mail-tab').forEach(function(tab) {
        tab.addEventListener('click', function() {
            var targetTab = tab.getAttribute('data-tab');
            if (targetTab === currentMailTab) return;

            // Update active tab
            document.querySelectorAll('.mail-tab').forEach(function(t) {
                t.classList.remove('active');
            });
            tab.classList.add('active');
            currentMailTab = targetTab;

            // Show/hide views
            if (targetTab === 'inbox') {
                mailList.style.display = 'block';
                mailAll.style.display = 'none';
            } else {
                mailList.style.display = 'none';
                mailAll.style.display = 'block';
            }

            // Hide detail/compose views
            mailDetail.style.display = 'none';
            mailCompose.style.display = 'none';
        });
    });

    // Load mail inbox as threaded conversations
    function loadMailInbox() {
        var loading = document.getElementById('mail-loading');
        var threadsContainer = document.getElementById('mail-threads');
        var empty = document.getElementById('mail-empty');
        var count = document.getElementById('mail-count');

        if (!loading || !threadsContainer) return;

        fetch('/api/mail/threads')
            .then(function(r) { return r.json(); })
            .then(function(data) {
                loading.style.display = 'none';

                if (data.threads && data.threads.length > 0) {
                    threadsContainer.style.display = 'block';
                    empty.style.display = 'none';
                    threadsContainer.innerHTML = '';

                    data.threads.forEach(function(thread) {
                        var threadEl = document.createElement('div');
                        threadEl.className = 'mail-thread' + (thread.unread_count > 0 ? ' mail-thread-unread' : '');

                        var last = thread.last_message;
                        var hasMultiple = thread.count > 1;
                        var countBadge = hasMultiple ? '<span class="thread-count">' + thread.count + '</span>' : '';
                        var unreadDot = thread.unread_count > 0 ? '<span class="thread-unread-dot"></span>' : '';

                        var priorityIcon = '';
                        if (last.priority === 'urgent') priorityIcon = '<span class="priority-urgent">⚡</span> ';
                        else if (last.priority === 'high') priorityIcon = '<span class="priority-high">!</span> ';

                        // Thread header (always visible)
                        var headerEl = document.createElement('div');
                        headerEl.className = 'mail-thread-header';
                        headerEl.setAttribute('data-thread-id', thread.thread_id);
                        headerEl.innerHTML =
                            '<div class="mail-thread-left">' +
                                unreadDot +
                                '<span class="mail-from">' + escapeHtml(last.from) + '</span>' +
                                countBadge +
                            '</div>' +
                            '<div class="mail-thread-center">' +
                                priorityIcon +
                                '<span class="mail-subject">' + escapeHtml(thread.subject) + '</span>' +
                                (hasMultiple ? '<span class="mail-thread-preview"> — ' + escapeHtml(last.body ? last.body.substring(0, 60) : '') + '</span>' : '') +
                            '</div>' +
                            '<div class="mail-thread-right">' +
                                '<span class="mail-time">' + formatMailTime(last.timestamp) + '</span>' +
                            '</div>';

                        threadEl.appendChild(headerEl);

                        // Thread messages (collapsed by default, only for multi-message threads)
                        if (hasMultiple) {
                            var msgsEl = document.createElement('div');
                            msgsEl.className = 'mail-thread-messages';
                            msgsEl.style.display = 'none';

                            thread.messages.forEach(function(msg) {
                                var msgEl = document.createElement('div');
                                msgEl.className = 'mail-thread-msg' + (msg.read ? '' : ' mail-unread');
                                msgEl.setAttribute('data-msg-id', msg.id);
                                msgEl.setAttribute('data-from', msg.from);
                                msgEl.innerHTML =
                                    '<div class="mail-thread-msg-header">' +
                                        '<span class="mail-from">' + escapeHtml(msg.from) + '</span>' +
                                        '<span class="mail-time">' + formatMailTime(msg.timestamp) + '</span>' +
                                    '</div>' +
                                    '<div class="mail-thread-msg-subject">' + escapeHtml(msg.subject) + '</div>';
                                msgsEl.appendChild(msgEl);
                            });

                            threadEl.appendChild(msgsEl);
                        } else {
                            // Single message thread - clicking opens the message directly
                            headerEl.setAttribute('data-msg-id', last.id);
                            headerEl.setAttribute('data-from', last.from);
                        }

                        threadsContainer.appendChild(threadEl);
                    });

                    // Update count
                    if (count) {
                        var unread = data.unread_count || 0;
                        count.textContent = unread > 0 ? unread + ' unread' : data.total;
                        if (unread > 0) count.classList.add('has-unread');
                        else count.classList.remove('has-unread');
                    }
                } else {
                    threadsContainer.style.display = 'none';
                    empty.style.display = 'block';
                    if (count) count.textContent = '0';
                }
            })
            .catch(function(err) {
                loading.textContent = 'Failed to load mail';
                console.error('Mail load error:', err);
            });
    }

    function formatMailTime(timestamp) {
        if (!timestamp) return '';
        var d = new Date(timestamp);
        var now = new Date();
        var diff = now - d;

        // Format: "Jan 26, 3:45 PM" or "Jan 26 2025, 3:45 PM" if different year
        var months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
        var month = months[d.getMonth()];
        var day = d.getDate();
        var hours = d.getHours();
        var minutes = d.getMinutes();
        var ampm = hours >= 12 ? 'PM' : 'AM';
        hours = hours % 12 || 12;
        var minStr = minutes < 10 ? '0' + minutes : minutes;
        var yearPart = d.getFullYear() !== now.getFullYear() ? ' ' + d.getFullYear() + ',' : '';
        var dateStr = month + ' ' + day + yearPart + ', ' + hours + ':' + minStr + ' ' + ampm;

        // Add relative time in parentheses for recent messages
        var relative = '';
        if (diff < 60000) relative = ' (just now)';
        else if (diff < 3600000) relative = ' (' + Math.floor(diff / 60000) + 'm ago)';
        else if (diff < 86400000) relative = ' (' + Math.floor(diff / 3600000) + 'h ago)';
        else if (diff < 604800000) relative = ' (' + Math.floor(diff / 86400000) + 'd ago)';

        return dateStr + relative;
    }

    // Load mail on page load
    loadMailInbox();

    // ============================================
    // CREW PANEL
    // ============================================
    function loadCrew() {
        var loading = document.getElementById('crew-loading');
        var table = document.getElementById('crew-table');
        var tbody = document.getElementById('crew-tbody');
        var empty = document.getElementById('crew-empty');
        var count = document.getElementById('crew-count');

        if (!loading || !table || !tbody) return;

        fetch('/api/crew')
            .then(function(r) { return r.json(); })
            .then(function(data) {
                loading.style.display = 'none';

                if (data.crew && data.crew.length > 0) {
                    table.style.display = 'table';
                    empty.style.display = 'none';
                    tbody.innerHTML = '';

                    // Check for state changes and notify
                    checkCrewNotifications(data.crew);

                    data.crew.forEach(function(member) {
                        var tr = document.createElement('tr');
                        var rowClass = 'crew-' + member.state;
                        tr.className = rowClass;

                        var stateClass = 'crew-state-' + member.state;
                        var stateText = member.state.charAt(0).toUpperCase() + member.state.slice(1);
                        var stateIcon = '';
                        if (member.state === 'spinning') stateIcon = '🔄 ';
                        else if (member.state === 'finished') stateIcon = '✅ ';
                        else if (member.state === 'questions') stateIcon = '❓ ';
                        else if (member.state === 'ready') stateIcon = '⏸️ ';

                        var sessionBadge = '';
                        if (member.session === 'attached') {
                            sessionBadge = '<span class="badge badge-green">Attached</span>';
                        } else if (member.session === 'detached') {
                            sessionBadge = '<span class="badge badge-muted">Detached</span>';
                        } else {
                            sessionBadge = '<span class="badge badge-muted">None</span>';
                        }

                        // Build the attach command based on the crew member's role
                        var attachCmd = 'gt crew at ' + member.name;
                        if (member.name === 'mayor') {
                            attachCmd = 'gt mayor attach';
                        } else if (member.name === 'deacon') {
                            attachCmd = 'gt deacon attach';
                        } else if (member.name === 'witness' || member.name.startsWith('witness-')) {
                            attachCmd = 'gt witness attach';
                        }

                        tr.innerHTML =
                            '<td><span class="crew-name">' + escapeHtml(member.name) + '</span></td>' +
                            '<td><span class="crew-rig">' + escapeHtml(member.rig) + '</span></td>' +
                            '<td><span class="' + stateClass + '">' + stateIcon + stateText + '</span></td>' +
                            '<td><span class="crew-hook">' + (member.hook ? escapeHtml(member.hook) : '—') + '</span></td>' +
                            '<td class="crew-activity">' + (member.last_active || '—') + '</td>' +
                            '<td>' + sessionBadge + '</td>' +
                            '<td><button class="attach-btn" data-cmd="' + escapeHtml(attachCmd) + '" title="Copy attach command">📎 Attach</button></td>';
                        tbody.appendChild(tr);
                    });

                    if (count) count.textContent = data.total;
                } else {
                    table.style.display = 'none';
                    empty.style.display = 'block';
                    if (count) count.textContent = '0';
                }
            })
            .catch(function(err) {
                loading.textContent = 'Failed to load crew';
                console.error('Crew load error:', err);
            });
    }

    // Track previous crew states for notifications
    var previousCrewStates = {};
    var crewNeedsAttention = 0;

    // Load crew on page load
    loadCrew();
    // Expose for refresh after HTMX swaps
    window.refreshCrewPanel = loadCrew;

    // Crew notification system - check for state changes
    function checkCrewNotifications(crewList) {
        var newNeedsAttention = 0;

        crewList.forEach(function(member) {
            var key = member.rig + '/' + member.name;
            var prevState = previousCrewStates[key];
            var newState = member.state;

            // Count crew needing attention
            if (newState === 'finished' || newState === 'questions') {
                newNeedsAttention++;
            }

            // Notify on state transitions to finished/questions
            if (prevState && prevState !== newState) {
                if (newState === 'finished') {
                    showToast('success', 'Crew Finished', member.name + ' finished their work!');
                    playNotificationSound();
                } else if (newState === 'questions') {
                    showToast('info', 'Needs Attention', member.name + ' has questions for you');
                    playNotificationSound();
                }
            }

            // Update stored state
            previousCrewStates[key] = newState;
        });

        // Update badge on crew panel
        crewNeedsAttention = newNeedsAttention;
        updateCrewBadge();
    }

    function updateCrewBadge() {
        var countEl = document.getElementById('crew-count');
        if (!countEl) return;

        // Add attention indicator if crew needs attention
        if (crewNeedsAttention > 0) {
            countEl.classList.add('needs-attention');
            countEl.setAttribute('data-attention', crewNeedsAttention);
        } else {
            countEl.classList.remove('needs-attention');
            countEl.removeAttribute('data-attention');
        }
    }

    function playNotificationSound() {
        // Simple beep using Web Audio API (optional, non-blocking)
        try {
            var ctx = new (window.AudioContext || window.webkitAudioContext)();
            var oscillator = ctx.createOscillator();
            var gain = ctx.createGain();
            oscillator.connect(gain);
            gain.connect(ctx.destination);
            oscillator.frequency.value = 800;
            gain.gain.value = 0.1;
            oscillator.start();
            oscillator.stop(ctx.currentTime + 0.1);
        } catch (e) {
            // Audio not available, ignore
        }
    }

    // Handle attach button clicks - copy command to clipboard
    document.addEventListener('click', function(e) {
        var btn = e.target.closest('.attach-btn');
        if (!btn) return;
        
        e.preventDefault();
        var cmd = btn.getAttribute('data-cmd');
        if (!cmd) return;

        navigator.clipboard.writeText(cmd).then(function() {
            showToast('success', 'Copied', cmd);
        }).catch(function() {
            // Fallback for older browsers
            showToast('info', 'Run in terminal', cmd);
        });
    });


    // ============================================
    // HOOK MANAGEMENT
    // ============================================

    function detachHook(btn) {
        var beadId = btn.getAttribute('data-hook-id');
        if (!beadId) return;

        if (!confirm('Detach hook ' + beadId + '?')) return;

        btn.disabled = true;
        btn.textContent = '...';

        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: 'hook detach ' + beadId, confirmed: true })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Detached', beadId + ' detached from hook');
                // Refresh the page to update the hooks panel
                if (typeof htmx !== 'undefined') {
                    htmx.trigger(document.body, 'htmx:load');
                }
            } else {
                showToast('error', 'Failed', data.error || 'Failed to detach hook');
                btn.disabled = false;
                btn.textContent = 'Detach';
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
            btn.disabled = false;
            btn.textContent = 'Detach';
        });
    }
    window.detachHook = detachHook;

    function openHookAttachForm() {
        var form = document.getElementById('hook-attach-form');
        if (form) {
            form.style.display = 'block';
            var input = document.getElementById('hook-attach-bead');
            if (input) {
                input.value = '';
                setTimeout(function() { input.focus(); }, 50);
            }
        }
    }
    window.openHookAttachForm = openHookAttachForm;

    function closeHookAttachForm() {
        var form = document.getElementById('hook-attach-form');
        if (form) {
            form.style.display = 'none';
        }
    }
    window.closeHookAttachForm = closeHookAttachForm;

    function submitHookAttach() {
        var input = document.getElementById('hook-attach-bead');
        var beadId = input ? input.value.trim() : '';

        if (!beadId) {
            showToast('error', 'Missing', 'Bead ID is required');
            return;
        }

        var submitBtn = document.querySelector('.hook-attach-submit');
        if (submitBtn) {
            submitBtn.disabled = true;
            submitBtn.textContent = '...';
        }

        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: 'hook attach ' + beadId, confirmed: true })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Attached', beadId + ' attached to hook');
                closeHookAttachForm();
                if (typeof htmx !== 'undefined') {
                    htmx.trigger(document.body, 'htmx:load');
                }
            } else {
                showToast('error', 'Failed', data.error || 'Failed to attach hook');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        })
        .finally(function() {
            if (submitBtn) {
                submitBtn.disabled = false;
                submitBtn.textContent = 'Attach';
            }
        });
    }
    window.submitHookAttach = submitHookAttach;

    function clearAllHooks() {
        if (!confirm('Clear ALL hooks? This will detach all hooked work.')) return;

        var rows = document.querySelectorAll('.hook-detach-btn');
        if (rows.length === 0) {
            showToast('info', 'Nothing', 'No hooks to clear');
            return;
        }

        var beadIds = [];
        for (var i = 0; i < rows.length; i++) {
            var id = rows[i].getAttribute('data-hook-id');
            if (id) beadIds.push(id);
        }

        var completed = 0;
        var errors = 0;

        beadIds.forEach(function(beadId) {
            fetch('/api/run', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ command: 'hook detach ' + beadId, confirmed: true })
            })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                if (data.success) {
                    completed++;
                } else {
                    errors++;
                }
            })
            .catch(function() {
                errors++;
            })
            .finally(function() {
                if (completed + errors === beadIds.length) {
                    if (errors > 0) {
                        showToast('error', 'Partial', completed + ' detached, ' + errors + ' failed');
                    } else {
                        showToast('success', 'Cleared', completed + ' hook(s) cleared');
                    }
                    if (typeof htmx !== 'undefined') {
                        htmx.trigger(document.body, 'htmx:load');
                    }
                }
            });
        });
    }
    window.clearAllHooks = clearAllHooks;

    // Handle Enter key in hook attach input
    document.addEventListener('keydown', function(e) {
        if (e.key === 'Enter' && e.target.id === 'hook-attach-bead') {
            e.preventDefault();
            submitHookAttach();
        }
        if (e.key === 'Escape' && e.target.id === 'hook-attach-bead') {
            e.preventDefault();
            closeHookAttachForm();
        }
    });

    // ============================================
    // ISSUE CREATION MODAL
    // ============================================
    function openIssueModal() {
        var modal = document.getElementById('issue-modal');
        if (modal) {
            modal.style.display = 'flex';
            window.pauseRefresh = true;
            // Focus the title input
            var titleInput = document.getElementById('issue-title');
            if (titleInput) {
                setTimeout(function() { titleInput.focus(); }, 100);
            }
        }
    }
    window.openIssueModal = openIssueModal;

    function closeIssueModal() {
        var modal = document.getElementById('issue-modal');
        if (modal) {
            modal.style.display = 'none';
            window.pauseRefresh = false;
            // Reset form
            var form = document.getElementById('issue-form');
            if (form) form.reset();
        }
    }
    window.closeIssueModal = closeIssueModal;

    function submitIssue(e) {
        e.preventDefault();
        
        var title = document.getElementById('issue-title').value.trim();
        var priority = document.getElementById('issue-priority').value;
        var description = document.getElementById('issue-description').value.trim();
        var submitBtn = document.getElementById('issue-submit-btn');

        if (!title) {
            showToast('error', 'Missing', 'Title is required');
            return;
        }

        // Disable button while submitting
        submitBtn.disabled = true;
        submitBtn.textContent = 'Creating...';

        var payload = {
            title: title,
            priority: parseInt(priority, 10)
        };
        if (description) {
            payload.description = description;
        }

        fetch('/api/issues/create', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Created', 'Issue ' + (data.id || '') + ' created');
                closeIssueModal();
                // Trigger a page refresh to show the new issue
                if (typeof htmx !== 'undefined') {
                    htmx.trigger(document.body, 'htmx:load');
                }
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        })
        .finally(function() {
            submitBtn.disabled = false;
            submitBtn.textContent = 'Create Issue';
        });
    }
    window.submitIssue = submitIssue;

    // Close modal on Escape key
    document.addEventListener('keydown', function(e) {
        if (e.key === 'Escape') {
            var modal = document.getElementById('issue-modal');
            if (modal && modal.style.display !== 'none') {
                closeIssueModal();
            }
        }
    });

    // ============================================
    // WORK PANEL TABS
    // ============================================
    function switchWorkTab(tab) {
        // Update active tab button
        document.querySelectorAll('.panel-tabs .tab-btn').forEach(function(btn) {
            btn.classList.remove('active');
            if (btn.getAttribute('data-tab') === tab) {
                btn.classList.add('active');
            }
        });

        // Filter rows based on tab
        var rows = document.querySelectorAll('#work-table tbody tr');
        rows.forEach(function(row) {
            var status = row.getAttribute('data-status') || 'ready';
            if (tab === 'all') {
                row.style.display = '';
            } else if (tab === 'ready' && status === 'ready') {
                row.style.display = '';
            } else if (tab === 'progress' && status === 'progress') {
                row.style.display = '';
            } else {
                row.style.display = 'none';
            }
        });

        // Update count
        var visibleCount = 0;
        rows.forEach(function(row) {
            if (row.style.display !== 'none') visibleCount++;
        });
        var countEl = document.querySelector('#work-panel .count');
        if (countEl) countEl.textContent = visibleCount;
    }
    window.switchWorkTab = switchWorkTab;

    // Initialize work panel to "Ready" tab on load
    setTimeout(function() {
        switchWorkTab('ready');
    }, 100);

    // ============================================
    // READY WORK PANEL
    // ============================================
    function loadReady() {
        var loading = document.getElementById('ready-loading');
        var table = document.getElementById('ready-table');
        var tbody = document.getElementById('ready-tbody');
        var empty = document.getElementById('ready-empty');
        var count = document.getElementById('ready-count');

        if (!loading || !table || !tbody) return;

        fetch('/api/ready')
            .then(function(r) { return r.json(); })
            .then(function(data) {
                loading.style.display = 'none';

                if (data.items && data.items.length > 0) {
                    table.style.display = 'table';
                    empty.style.display = 'none';
                    tbody.innerHTML = '';

                    data.items.forEach(function(item) {
                        var tr = document.createElement('tr');
                        var rowClass = '';
                        if (item.priority === 1) rowClass = 'ready-p1';
                        else if (item.priority === 2) rowClass = 'ready-p2';
                        tr.className = rowClass;

                        var priBadge = '';
                        if (item.priority === 1) priBadge = '<span class="badge badge-red">P1</span>';
                        else if (item.priority === 2) priBadge = '<span class="badge badge-orange">P2</span>';
                        else if (item.priority === 3) priBadge = '<span class="badge badge-yellow">P3</span>';
                        else priBadge = '<span class="badge badge-muted">P4</span>';

                        var sourceClass = item.source === 'town' ? 'ready-source ready-source-town' : 'ready-source';

                        tr.innerHTML =
                            '<td>' + priBadge + '</td>' +
                            '<td><span class="ready-id">' + escapeHtml(item.id) + '</span></td>' +
                            '<td><span class="ready-title">' + escapeHtml(item.title || '') + '</span></td>' +
                            '<td><span class="' + sourceClass + '">' + escapeHtml(item.source) + '</span></td>' +
                            '<td><button class="sling-btn" data-bead-id="' + escapeHtml(item.id) + '" title="Sling to rig">Sling</button></td>';
                        tbody.appendChild(tr);
                    });

                    if (count) count.textContent = data.summary.total;
                } else {
                    table.style.display = 'none';
                    empty.style.display = 'block';
                    if (count) count.textContent = '0';
                }
            })
            .catch(function(err) {
                loading.textContent = 'Failed to load ready work';
                console.error('Ready work load error:', err);
            });
    }

    // Load ready work on page load
    loadReady();
    // Expose for refresh after HTMX swaps
    window.refreshReadyPanel = loadReady;

    // ============================================
    // CONVOY PANEL INTERACTIONS
    // ============================================
    var convoyList = document.getElementById('convoy-list');
    var convoyDetail = document.getElementById('convoy-detail');
    var convoyCreateForm = document.getElementById('convoy-create-form');
    var currentConvoyId = null;

    // Click on convoy row to view details
    document.addEventListener('click', function(e) {
        var convoyRow = e.target.closest('.convoy-row');
        if (convoyRow && convoyRow.hasAttribute('data-convoy-id')) {
            e.preventDefault();
            var convoyId = convoyRow.getAttribute('data-convoy-id');
            if (convoyId) {
                openConvoyDetail(convoyId);
            }
        }
    });

    function openConvoyDetail(convoyId) {
        currentConvoyId = convoyId;
        window.pauseRefresh = true;

        // Reset views
        document.getElementById('convoy-detail-id').textContent = convoyId;
        document.getElementById('convoy-detail-title').textContent = 'Convoy: ' + convoyId;
        document.getElementById('convoy-detail-status').textContent = '';
        document.getElementById('convoy-detail-progress').textContent = '';
        document.getElementById('convoy-issues-loading').style.display = 'block';
        document.getElementById('convoy-issues-table').style.display = 'none';
        document.getElementById('convoy-issues-empty').style.display = 'none';
        document.getElementById('convoy-add-issue-form').style.display = 'none';

        // Show detail, hide list and create form
        convoyList.style.display = 'none';
        convoyCreateForm.style.display = 'none';
        convoyDetail.style.display = 'block';

        // Fetch convoy status via /api/run
        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: 'convoy status ' + convoyId })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            document.getElementById('convoy-issues-loading').style.display = 'none';

            if (!data.success) {
                document.getElementById('convoy-issues-empty').style.display = 'block';
                document.getElementById('convoy-issues-empty').querySelector('p').textContent = data.error || 'Failed to load convoy';
                return;
            }

            var issues = parseConvoyStatusOutput(data.output || '');
            if (issues.length === 0) {
                document.getElementById('convoy-issues-empty').style.display = 'block';
                return;
            }

            var tbody = document.getElementById('convoy-issues-tbody');
            tbody.innerHTML = '';
            issues.forEach(function(issue) {
                var tr = document.createElement('tr');
                var statusBadge = '';
                var statusLower = (issue.status || '').toLowerCase();
                if (statusLower === 'closed' || statusLower === 'complete' || statusLower === 'done') {
                    statusBadge = '<span class="badge badge-green">Done</span>';
                } else if (statusLower === 'in_progress' || statusLower === 'in progress' || statusLower === 'working') {
                    statusBadge = '<span class="badge badge-yellow">In Progress</span>';
                } else if (statusLower === 'open' || statusLower === 'ready') {
                    statusBadge = '<span class="badge badge-blue">Open</span>';
                } else if (statusLower === 'blocked') {
                    statusBadge = '<span class="badge badge-red">Blocked</span>';
                } else {
                    statusBadge = '<span class="badge badge-muted">' + escapeHtml(issue.status || 'Unknown') + '</span>';
                }

                tr.innerHTML =
                    '<td class="convoy-issue-status">' + statusBadge + '</td>' +
                    '<td><span class="issue-id">' + escapeHtml(issue.id) + '</span></td>' +
                    '<td class="issue-title">' + escapeHtml(issue.title || '') + '</td>' +
                    '<td>' + (issue.assignee ? '<span class="badge badge-blue">' + escapeHtml(issue.assignee) + '</span>' : '<span class="badge badge-muted">Unassigned</span>') + '</td>' +
                    '<td>' + escapeHtml(issue.progress || '') + '</td>';
                tbody.appendChild(tr);
            });
            document.getElementById('convoy-issues-table').style.display = 'table';
        })
        .catch(function(err) {
            document.getElementById('convoy-issues-loading').style.display = 'none';
            document.getElementById('convoy-issues-empty').style.display = 'block';
            document.getElementById('convoy-issues-empty').querySelector('p').textContent = 'Error: ' + err.message;
        });
    }

    // Parse convoy status text output into issue objects
    function parseConvoyStatusOutput(output) {
        var issues = [];
        var lines = output.split('\n');
        for (var i = 0; i < lines.length; i++) {
            var line = lines[i].trim();
            if (!line) continue;
            // Skip header lines and convoy summary lines
            if (line.startsWith('Convoy') || line.startsWith('===') || line.startsWith('---') ||
                line.startsWith('Status:') || line.startsWith('Progress:') || line.startsWith('Created:') ||
                line.startsWith('Title:') || line.startsWith('Issues:') || line.startsWith('Name:')) {
                // Extract convoy-level status/progress for the detail header
                if (line.startsWith('Status:')) {
                    var statusEl = document.getElementById('convoy-detail-status');
                    var statusVal = line.replace('Status:', '').trim().toLowerCase();
                    statusEl.textContent = statusVal;
                    statusEl.className = 'badge';
                    if (statusVal === 'active') statusEl.classList.add('badge-green');
                    else if (statusVal === 'stale') statusEl.classList.add('badge-yellow');
                    else if (statusVal === 'stuck') statusEl.classList.add('badge-red');
                    else if (statusVal === 'complete') statusEl.classList.add('badge-green');
                    else statusEl.classList.add('badge-muted');
                }
                if (line.startsWith('Progress:')) {
                    document.getElementById('convoy-detail-progress').textContent = line.replace('Progress:', '').trim();
                }
                continue;
            }
            // Look for issue lines - typically formatted as:
            // "○ id · title [● P2 · STATUS]" or similar bead-style output
            // Or tabular: "id   title   status   assignee"
            var issue = parseConvoyIssueLine(line);
            if (issue) {
                issues.push(issue);
            }
        }
        return issues;
    }

    // Parse a single issue line from convoy status output
    function parseConvoyIssueLine(line) {
        // Try bead-style format: "○ id · title   [● P2 · OPEN]"
        // or "◐ id · title   [● P2 · IN_PROGRESS]"
        var beadMatch = line.match(/^[○◐●✓]\s+(\S+)\s+[·:]\s+(.+?)(?:\s+\[.*?([A-Z_]+)\])?$/);
        if (beadMatch) {
            var statusFromBracket = '';
            if (beadMatch[3]) {
                statusFromBracket = beadMatch[3].toLowerCase().replace('_', ' ');
            } else {
                // Infer from icon
                if (line.startsWith('✓')) statusFromBracket = 'closed';
                else if (line.startsWith('◐')) statusFromBracket = 'in progress';
                else statusFromBracket = 'open';
            }
            return {
                id: beadMatch[1],
                title: beadMatch[2].trim(),
                status: statusFromBracket,
                assignee: '',
                progress: ''
            };
        }

        // Try simple "id title" format (at least an ID-like token)
        var parts = line.split(/\s{2,}/);
        if (parts.length >= 2 && parts[0].match(/^[a-zA-Z0-9_-]+$/)) {
            return {
                id: parts[0],
                title: parts[1] || '',
                status: parts[2] || '',
                assignee: parts[3] || '',
                progress: parts[4] || ''
            };
        }

        return null;
    }

    // Back button from convoy detail
    document.getElementById('convoy-back-btn').addEventListener('click', function() {
        convoyDetail.style.display = 'none';
        convoyList.style.display = 'block';
        currentConvoyId = null;
        window.pauseRefresh = false;
    });

    // New Convoy button
    document.getElementById('new-convoy-btn').addEventListener('click', function() {
        window.pauseRefresh = true;
        convoyList.style.display = 'none';
        convoyDetail.style.display = 'none';
        convoyCreateForm.style.display = 'block';
        document.getElementById('convoy-create-name').value = '';
        document.getElementById('convoy-create-issues').value = '';
        document.getElementById('convoy-create-name').focus();
    });

    // Cancel create convoy
    document.getElementById('convoy-create-back-btn').addEventListener('click', cancelConvoyCreate);
    document.getElementById('convoy-create-cancel-btn').addEventListener('click', cancelConvoyCreate);

    function cancelConvoyCreate() {
        convoyCreateForm.style.display = 'none';
        convoyList.style.display = 'block';
        window.pauseRefresh = false;
    }

    // Submit create convoy
    document.getElementById('convoy-create-submit-btn').addEventListener('click', function() {
        var name = document.getElementById('convoy-create-name').value.trim();
        var issuesStr = document.getElementById('convoy-create-issues').value.trim();

        if (!name) {
            showToast('error', 'Missing', 'Convoy name is required');
            return;
        }

        var btn = document.getElementById('convoy-create-submit-btn');
        btn.disabled = true;
        btn.textContent = 'Creating...';

        // Build command: convoy create <name> [issue1 issue2 ...]
        var cmd = 'convoy create ' + name;
        if (issuesStr) {
            cmd += ' ' + issuesStr;
        }

        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: cmd, confirmed: true, timeout: 120 })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Created', 'Convoy "' + name + '" created');
                cancelConvoyCreate();
                if (data.output && data.output.trim()) {
                    showOutput(cmd, data.output);
                }
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        })
        .finally(function() {
            btn.disabled = false;
            btn.textContent = 'Create Convoy';
        });
    });

    // Add Issue button in convoy detail
    document.getElementById('convoy-add-issue-btn').addEventListener('click', function() {
        var form = document.getElementById('convoy-add-issue-form');
        form.style.display = form.style.display === 'none' ? 'flex' : 'none';
        if (form.style.display !== 'none') {
            document.getElementById('convoy-add-issue-input').value = '';
            document.getElementById('convoy-add-issue-input').focus();
        }
    });

    // Cancel add issue
    document.getElementById('convoy-add-issue-cancel').addEventListener('click', function() {
        document.getElementById('convoy-add-issue-form').style.display = 'none';
    });

    // Submit add issue to convoy
    document.getElementById('convoy-add-issue-submit').addEventListener('click', submitAddIssueToConvoy);

    // Enter key in add issue input
    document.getElementById('convoy-add-issue-input').addEventListener('keydown', function(e) {
        if (e.key === 'Enter') {
            e.preventDefault();
            submitAddIssueToConvoy();
        } else if (e.key === 'Escape') {
            e.preventDefault();
            document.getElementById('convoy-add-issue-form').style.display = 'none';
        }
    });

    function submitAddIssueToConvoy() {
        var issueId = document.getElementById('convoy-add-issue-input').value.trim();
        if (!issueId || !currentConvoyId) {
            showToast('error', 'Missing', 'Issue ID is required');
            return;
        }

        var btn = document.getElementById('convoy-add-issue-submit');
        btn.disabled = true;
        btn.textContent = 'Adding...';

        var cmd = 'convoy add ' + currentConvoyId + ' ' + issueId;

        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: cmd, confirmed: true })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Added', 'Issue ' + issueId + ' added to convoy');
                document.getElementById('convoy-add-issue-form').style.display = 'none';
                // Refresh the convoy detail view
                openConvoyDetail(currentConvoyId);
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        })
        .finally(function() {
            btn.disabled = false;
            btn.textContent = 'Add';
        });
    }

    // Click on mail thread header - toggle expand or open single message
    document.addEventListener('click', function(e) {
        // Handle click on individual message within expanded thread
        var threadMsg = e.target.closest('.mail-thread-msg');
        if (threadMsg) {
            e.preventDefault();
            var msgId = threadMsg.getAttribute('data-msg-id');
            var from = threadMsg.getAttribute('data-from');
            if (msgId) {
                openMailDetail(msgId, from);
            }
            return;
        }

        // Handle click on thread header
        var threadHeader = e.target.closest('.mail-thread-header');
        if (threadHeader) {
            e.preventDefault();
            var msgId = threadHeader.getAttribute('data-msg-id');
            if (msgId) {
                // Single message thread - open directly
                var from = threadHeader.getAttribute('data-from');
                openMailDetail(msgId, from);
            } else {
                // Multi-message thread - toggle expand/collapse
                var threadEl = threadHeader.closest('.mail-thread');
                var msgsEl = threadEl ? threadEl.querySelector('.mail-thread-messages') : null;
                if (msgsEl) {
                    var isExpanded = msgsEl.style.display !== 'none';
                    msgsEl.style.display = isExpanded ? 'none' : 'block';
                    threadEl.classList.toggle('mail-thread-expanded', !isExpanded);
                }
            }
            return;
        }

        // Legacy: handle click on mail-row (All Traffic tab)
        var mailRow = e.target.closest('.mail-row');
        if (mailRow) {
            e.preventDefault();
            var msgId = mailRow.getAttribute('data-msg-id');
            var from = mailRow.getAttribute('data-from');
            if (msgId) {
                openMailDetail(msgId, from);
            }
        }
    });

    function openMailDetail(msgId, from) {
        currentMessageId = msgId;
        currentMessageFrom = from;

        // Pause HTMX refresh while viewing/composing mail
        window.pauseRefresh = true;

        // Show loading state
        document.getElementById('mail-detail-subject').textContent = 'Loading...';
        document.getElementById('mail-detail-from').textContent = from || '';
        document.getElementById('mail-detail-body').textContent = '';
        document.getElementById('mail-detail-time').textContent = '';

        // Hide both list views and compose, show detail
        mailList.style.display = 'none';
        if (mailAll) mailAll.style.display = 'none';
        mailCompose.style.display = 'none';
        mailDetail.style.display = 'block';

        // Fetch message content
        fetch('/api/mail/read?id=' + encodeURIComponent(msgId))
            .then(function(r) { return r.json(); })
            .then(function(msg) {
                document.getElementById('mail-detail-subject').textContent = msg.subject || '(no subject)';
                document.getElementById('mail-detail-from').textContent = msg.from || from;
                document.getElementById('mail-detail-body').textContent = msg.body || '(no content)';
                document.getElementById('mail-detail-time').textContent = msg.timestamp || '';
            })
            .catch(function(err) {
                document.getElementById('mail-detail-body').textContent = 'Error loading message: ' + err.message;
            });
    }

    // Back button from detail view - return to correct tab
    document.getElementById('mail-back-btn').addEventListener('click', function() {
        mailDetail.style.display = 'none';
        mailCompose.style.display = 'none';

        // Return to the correct view based on current tab
        if (currentMailTab === 'all' && mailAll) {
            mailAll.style.display = 'block';
            mailList.style.display = 'none';
        } else {
            mailList.style.display = 'block';
            if (mailAll) mailAll.style.display = 'none';
        }

        currentMessageId = null;
        currentMessageFrom = null;
        // Resume HTMX refresh
        window.pauseRefresh = false;
    });

    // Reply button
    document.getElementById('mail-reply-btn').addEventListener('click', function() {
        var subject = document.getElementById('mail-detail-subject').textContent;
        var replySubject = subject.startsWith('Re: ') ? subject : 'Re: ' + subject;

        document.getElementById('mail-compose-title').textContent = 'Reply';
        document.getElementById('compose-subject').value = replySubject;
        document.getElementById('compose-reply-to').value = currentMessageId || '';
        document.getElementById('compose-body').value = '';

        // Populate To dropdown and select the sender
        populateToDropdown(currentMessageFrom);

        mailDetail.style.display = 'none';
        mailCompose.style.display = 'block';
        document.getElementById('compose-body').focus();
    });

    // Compose new message button
    document.getElementById('compose-mail-btn').addEventListener('click', function() {
        // Pause HTMX refresh while composing
        window.pauseRefresh = true;

        document.getElementById('mail-compose-title').textContent = 'New Message';
        document.getElementById('compose-subject').value = '';
        document.getElementById('compose-body').value = '';
        document.getElementById('compose-reply-to').value = '';

        // Populate To dropdown
        populateToDropdown(null);

        // Hide all mail views, show compose
        mailList.style.display = 'none';
        if (mailAll) mailAll.style.display = 'none';
        mailDetail.style.display = 'none';
        mailCompose.style.display = 'block';
        document.getElementById('compose-to').focus();
    });

    // Back button from compose view
    document.getElementById('compose-back-btn').addEventListener('click', function() {
        mailCompose.style.display = 'none';
        if (currentMessageId) {
            mailDetail.style.display = 'block';
        } else if (currentMailTab === 'all' && mailAll) {
            mailAll.style.display = 'block';
        } else {
            mailList.style.display = 'block';
        }
    });

    // Cancel compose
    document.getElementById('compose-cancel-btn').addEventListener('click', function() {
        mailCompose.style.display = 'none';
        mailList.style.display = 'block';
        currentMessageId = null;
        currentMessageFrom = null;
        // Resume HTMX refresh
        window.pauseRefresh = false;
    });

    // Send message
    document.getElementById('mail-send-btn').addEventListener('click', function() {
        var to = document.getElementById('compose-to').value;
        var subject = document.getElementById('compose-subject').value;
        var body = document.getElementById('compose-body').value;
        var replyTo = document.getElementById('compose-reply-to').value;

        if (!to || !subject) {
            showToast('error', 'Missing fields', 'Please fill in To and Subject');
            return;
        }

        var btn = document.getElementById('mail-send-btn');
        btn.textContent = 'Sending...';
        btn.disabled = true;

        fetch('/api/mail/send', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                to: to,
                subject: subject,
                body: body,
                reply_to: replyTo || undefined
            })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Sent', 'Message sent to ' + to);
                mailCompose.style.display = 'none';
                mailList.style.display = 'block';
                currentMessageId = null;
                currentMessageFrom = null;
                // Resume HTMX refresh and reload inbox
                window.pauseRefresh = false;
                loadMailInbox();
            } else {
                showToast('error', 'Failed', data.error || 'Failed to send message');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        })
        .finally(function() {
            btn.textContent = 'Send';
            btn.disabled = false;
        });
    });

    // Populate To dropdown with agents
    // Returns a Promise so callers can wait for it
    function populateToDropdown(selectedValue) {
        var select = document.getElementById('compose-to');
        
        // Show loading state
        select.innerHTML = '<option value="">⏳ Loading recipients...</option>';
        select.disabled = true;

        // If we have a selected value for reply, add it immediately so it's available
        if (selectedValue) {
            var cleanValue = selectedValue.replace(/\/$/, '').trim();
            var opt = document.createElement('option');
            opt.value = cleanValue;
            opt.textContent = cleanValue + ' (replying to)';
            opt.selected = true;
            select.appendChild(opt);
            select.disabled = false;
        }

        // Fetch agents from options API
        return fetch('/api/options')
            .then(function(r) { return r.json(); })
            .then(function(data) {
                // Clear loading state, rebuild options
                select.innerHTML = '<option value="">Select recipient...</option>';
                
                // Re-add reply-to if present
                if (selectedValue) {
                    var cleanVal = selectedValue.replace(/\/$/, '').trim();
                    var replyOpt = document.createElement('option');
                    replyOpt.value = cleanVal;
                    replyOpt.textContent = cleanVal + ' (replying to)';
                    replyOpt.selected = true;
                    select.appendChild(replyOpt);
                }
                
                var agents = data.agents || [];
                var addedValues = selectedValue ? [selectedValue.replace(/\/$/, '').toLowerCase()] : [];

                agents.forEach(function(agent) {
                    var name = typeof agent === 'string' ? agent : agent.name;
                    var running = typeof agent === 'object' ? agent.running : true;

                    // Skip if already added as reply-to
                    if (addedValues.indexOf(name.toLowerCase()) !== -1) {
                        return;
                    }

                    var opt = document.createElement('option');
                    opt.value = name;
                    opt.textContent = name + (running ? ' (● running)' : ' (○ stopped)');
                    if (!running) opt.disabled = true;
                    select.appendChild(opt);
                });
                
                select.disabled = false;
            })
            .catch(function(err) {
                console.error('Failed to load agents for To dropdown:', err);
                select.innerHTML = '<option value="">⚠ Failed to load recipients</option>';
                select.disabled = false;
            });
    }

    // ============================================
    // ISSUE PANEL INTERACTIONS
    // ============================================
    var issuesList = document.getElementById('issues-list');
    var issueDetail = document.getElementById('issue-detail');
    var currentIssueId = null;

    // Click on issue row to view details
    document.addEventListener('click', function(e) {
        var issueRow = e.target.closest('.issue-row');
        if (issueRow && issueRow.hasAttribute('data-issue-id')) {
            e.preventDefault();
            var issueId = issueRow.getAttribute('data-issue-id');
            if (issueId) {
                openIssueDetail(issueId);
            }
        }

        // Click on dependency links
        var depItem = e.target.closest('.issue-dep-item');
        if (depItem) {
            e.preventDefault();
            var depId = depItem.getAttribute('data-issue-id');
            if (depId) {
                openIssueDetail(depId);
            }
        }
    });

    function openIssueDetail(issueId) {
        currentIssueId = issueId;

        // Pause HTMX refresh while viewing issue
        window.pauseRefresh = true;

        // Show loading state
        document.getElementById('issue-detail-id').textContent = issueId;
        document.getElementById('issue-detail-title-text').textContent = 'Loading...';
        document.getElementById('issue-detail-description').textContent = '';
        document.getElementById('issue-detail-priority').textContent = '';
        document.getElementById('issue-detail-status').textContent = '';
        document.getElementById('issue-detail-type').textContent = '';
        document.getElementById('issue-detail-created').textContent = '';
        document.getElementById('issue-detail-owner').textContent = '';
        document.getElementById('issue-detail-actions').innerHTML = '';
        document.getElementById('issue-detail-depends-on').innerHTML = '';
        document.getElementById('issue-detail-blocks').innerHTML = '';
        document.getElementById('issue-detail-deps').style.display = 'none';
        document.getElementById('issue-detail-blocks-section').style.display = 'none';

        // Show detail view
        issuesList.style.display = 'none';
        issueDetail.style.display = 'block';

        // Fetch issue details
        fetch('/api/issues/show?id=' + encodeURIComponent(issueId))
            .then(function(r) { return r.json(); })
            .then(function(data) {
                if (data.error) {
                    document.getElementById('issue-detail-title-text').textContent = 'Error loading issue';
                    document.getElementById('issue-detail-description').textContent = data.error;
                    return;
                }

                document.getElementById('issue-detail-id').textContent = data.id || issueId;
                document.getElementById('issue-detail-title-text').textContent = data.title || '(no title)';
                document.getElementById('issue-detail-description').textContent = data.description || data.raw_output || '(no description)';

                // Priority badge
                var priorityEl = document.getElementById('issue-detail-priority');
                if (data.priority) {
                    priorityEl.textContent = data.priority;
                    priorityEl.className = 'badge';
                    if (data.priority === 'P1') priorityEl.classList.add('badge-red');
                    else if (data.priority === 'P2') priorityEl.classList.add('badge-orange');
                    else if (data.priority === 'P3') priorityEl.classList.add('badge-yellow');
                    else priorityEl.classList.add('badge-muted');
                }

                // Status
                var statusEl = document.getElementById('issue-detail-status');
                if (data.status) {
                    statusEl.textContent = data.status;
                    statusEl.className = 'issue-status ' + data.status.toLowerCase().replace(' ', '_');
                }

                // Meta info
                if (data.type) {
                    document.getElementById('issue-detail-type').textContent = 'Type: ' + data.type;
                }
                if (data.owner) {
                    document.getElementById('issue-detail-owner').textContent = 'Owner: ' + data.owner;
                }
                if (data.created) {
                    document.getElementById('issue-detail-created').textContent = 'Created: ' + data.created;
                }

                // Render action buttons
                renderIssueActions(issueId, data);

                // Dependencies
                if (data.depends_on && data.depends_on.length > 0) {
                    document.getElementById('issue-detail-deps').style.display = 'block';
                    var depsHtml = data.depends_on.map(function(dep) {
                        return '<span class="issue-dep-item" data-issue-id="' + escapeHtml(dep) + '">→ ' + escapeHtml(dep) + '</span>';
                    }).join(' ');
                    document.getElementById('issue-detail-depends-on').innerHTML = depsHtml;
                }

                // Blocks
                if (data.blocks && data.blocks.length > 0) {
                    document.getElementById('issue-detail-blocks-section').style.display = 'block';
                    var blocksHtml = data.blocks.map(function(dep) {
                        return '<span class="issue-dep-item" data-issue-id="' + escapeHtml(dep) + '">← ' + escapeHtml(dep) + '</span>';
                    }).join(' ');
                    document.getElementById('issue-detail-blocks').innerHTML = blocksHtml;
                }
            })
            .catch(function(err) {
                document.getElementById('issue-detail-title-text').textContent = 'Error';
                document.getElementById('issue-detail-description').textContent = 'Failed to load issue: ' + err.message;
            });
    }

    // Back button from issue detail
    var issueBackBtn = document.getElementById('issue-back-btn');
    if (issueBackBtn) {
        issueBackBtn.addEventListener('click', function() {
            issueDetail.style.display = 'none';
            issuesList.style.display = 'block';
            currentIssueId = null;
            // Resume HTMX refresh
            window.pauseRefresh = false;
        });
    }

    // ============================================
    // ISSUE ACTION BUTTONS
    // ============================================

    // Render action buttons based on current issue state
    function renderIssueActions(issueId, data) {
        var actionsEl = document.getElementById('issue-detail-actions');
        if (!actionsEl) return;

        var status = (data.status || '').toUpperCase();
        var isClosed = status === 'CLOSED';
        var currentPriority = data.priority || 'P2';
        // Extract numeric priority (P1 -> 1, P2 -> 2, etc.)
        var priNum = currentPriority.length === 2 ? parseInt(currentPriority[1], 10) : 2;

        var html = '<div class="issue-actions-bar">';

        // Close / Reopen button
        if (isClosed) {
            html += '<button class="issue-action-btn reopen" onclick="reopenIssue(\'' + escapeHtml(issueId) + '\')">↺ Reopen</button>';
        } else {
            html += '<button class="issue-action-btn close" onclick="closeIssue(\'' + escapeHtml(issueId) + '\')">✓ Close</button>';
        }

        // Priority dropdown
        html += '<div class="issue-action-group">';
        html += '<label class="issue-action-label">Priority</label>';
        html += '<select class="issue-action-select" id="issue-action-priority" onchange="updateIssuePriority(\'' + escapeHtml(issueId) + '\', this.value)">';
        for (var p = 1; p <= 4; p++) {
            var sel = p === priNum ? ' selected' : '';
            var pLabel = p === 1 ? 'P1 - Critical' : p === 2 ? 'P2 - High' : p === 3 ? 'P3 - Medium' : 'P4 - Low';
            html += '<option value="' + p + '"' + sel + '>' + pLabel + '</option>';
        }
        html += '</select>';
        html += '</div>';

        // Assignee dropdown
        html += '<div class="issue-action-group">';
        html += '<label class="issue-action-label">Assign</label>';
        html += '<select class="issue-action-select" id="issue-action-assignee" onchange="assignIssue(\'' + escapeHtml(issueId) + '\', this.value)">';
        html += '<option value="">Unassigned</option>';
        html += '<option value="" disabled>Loading agents...</option>';
        html += '</select>';
        html += '</div>';

        html += '</div>';
        actionsEl.innerHTML = html;

        // Load agents for assignee dropdown
        loadAssigneeOptions(data.owner || '');
    }

    // Load agent options into the assignee dropdown
    function loadAssigneeOptions(currentOwner) {
        var select = document.getElementById('issue-action-assignee');
        if (!select) return;

        fetch('/api/options')
            .then(function(r) { return r.json(); })
            .then(function(data) {
                // Rebuild dropdown
                var html = '<option value="">Unassigned</option>';
                var agents = data.agents || [];
                var polecats = data.polecats || [];

                // Combine agents and polecats for assignee options
                var seen = {};
                var allOptions = [];

                agents.forEach(function(agent) {
                    var name = typeof agent === 'string' ? agent : agent.name;
                    if (!seen[name]) {
                        seen[name] = true;
                        allOptions.push(name);
                    }
                });

                polecats.forEach(function(polecat) {
                    if (!seen[polecat]) {
                        seen[polecat] = true;
                        allOptions.push(polecat);
                    }
                });

                allOptions.forEach(function(name) {
                    var sel = name === currentOwner ? ' selected' : '';
                    html += '<option value="' + escapeHtml(name) + '"' + sel + '>' + escapeHtml(name) + '</option>';
                });

                select.innerHTML = html;
            })
            .catch(function() {
                select.innerHTML = '<option value="">Unassigned</option>';
            });
    }

    // Close an issue
    function closeIssue(issueId) {
        if (!confirm('Close issue ' + issueId + '?')) return;

        showToast('info', 'Closing...', issueId);

        fetch('/api/issues/close', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ id: issueId })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Closed', issueId + ' closed');
                // Re-fetch to update the detail view
                openIssueDetail(issueId);
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        });
    }
    window.closeIssue = closeIssue;

    // Reopen an issue
    function reopenIssue(issueId) {
        showToast('info', 'Reopening...', issueId);

        fetch('/api/issues/update', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ id: issueId, status: 'open' })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Reopened', issueId + ' reopened');
                openIssueDetail(issueId);
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        });
    }
    window.reopenIssue = reopenIssue;

    // Update issue priority
    function updateIssuePriority(issueId, priority) {
        var priNum = parseInt(priority, 10);
        if (priNum < 1 || priNum > 4) return;

        showToast('info', 'Updating...', 'Setting priority to P' + priNum);

        fetch('/api/issues/update', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ id: issueId, priority: priNum })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Updated', 'Priority set to P' + priNum);
                openIssueDetail(issueId);
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        });
    }
    window.updateIssuePriority = updateIssuePriority;

    // Assign issue to agent
    function assignIssue(issueId, assignee) {
        if (!assignee) return; // Unassigned selected, no-op for now

        showToast('info', 'Assigning...', 'Assigning to ' + assignee);

        fetch('/api/issues/update', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ id: issueId, assignee: assignee })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Assigned', 'Assigned to ' + assignee);
                openIssueDetail(issueId);
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message);
        });
    }
    window.assignIssue = assignIssue;

    // ============================================
    // PR/MERGE QUEUE PANEL INTERACTIONS
    // ============================================
    var prList = document.getElementById('pr-list');
    var prDetail = document.getElementById('pr-detail');
    var currentPrUrl = null;

    // Click on PR row to view details
    document.addEventListener('click', function(e) {
        var prRow = e.target.closest('.pr-row');
        if (prRow && prRow.hasAttribute('data-pr-url')) {
            e.preventDefault();
            var prUrl = prRow.getAttribute('data-pr-url');
            if (prUrl) {
                openPrDetail(prUrl);
            }
        }
    });

    function openPrDetail(prUrl) {
        currentPrUrl = prUrl;

        // Pause HTMX refresh while viewing PR
        window.pauseRefresh = true;

        // Show loading state
        document.getElementById('pr-detail-number').textContent = 'Loading...';
        document.getElementById('pr-detail-title-text').textContent = '';
        document.getElementById('pr-detail-body').textContent = '';
        document.getElementById('pr-detail-state').textContent = '';
        document.getElementById('pr-detail-author').textContent = '';
        document.getElementById('pr-detail-branches').textContent = '';
        document.getElementById('pr-detail-created').textContent = '';
        document.getElementById('pr-detail-additions').textContent = '';
        document.getElementById('pr-detail-deletions').textContent = '';
        document.getElementById('pr-detail-files').textContent = '';
        document.getElementById('pr-detail-labels').innerHTML = '';
        document.getElementById('pr-detail-checks').innerHTML = '';
        document.getElementById('pr-detail-labels-section').style.display = 'none';
        document.getElementById('pr-detail-checks-section').style.display = 'none';
        document.getElementById('pr-detail-link').href = prUrl;

        // Show detail view
        prList.style.display = 'none';
        prDetail.style.display = 'block';

        // Fetch PR details
        fetch('/api/pr/show?url=' + encodeURIComponent(prUrl))
            .then(function(r) { return r.json(); })
            .then(function(data) {
                if (data.error) {
                    document.getElementById('pr-detail-title-text').textContent = 'Error loading PR';
                    document.getElementById('pr-detail-body').textContent = data.error;
                    return;
                }

                document.getElementById('pr-detail-number').textContent = '#' + data.number;
                document.getElementById('pr-detail-title-text').textContent = data.title || '(no title)';
                document.getElementById('pr-detail-body').textContent = data.body || '(no description)';

                // State badge
                var stateEl = document.getElementById('pr-detail-state');
                if (data.state) {
                    stateEl.textContent = data.state;
                    stateEl.className = 'pr-state ' + data.state.toLowerCase();
                }

                // Meta info
                if (data.author) {
                    document.getElementById('pr-detail-author').textContent = 'by ' + data.author;
                }
                if (data.base_ref && data.head_ref) {
                    document.getElementById('pr-detail-branches').textContent = data.head_ref + ' → ' + data.base_ref;
                }
                if (data.created_at) {
                    var created = new Date(data.created_at);
                    document.getElementById('pr-detail-created').textContent = 'Created ' + created.toLocaleDateString();
                }

                // Stats
                if (data.additions !== undefined) {
                    document.getElementById('pr-detail-additions').textContent = '+' + data.additions;
                }
                if (data.deletions !== undefined) {
                    document.getElementById('pr-detail-deletions').textContent = '-' + data.deletions;
                }
                if (data.changed_files !== undefined) {
                    document.getElementById('pr-detail-files').textContent = data.changed_files + ' files';
                }

                // Labels
                if (data.labels && data.labels.length > 0) {
                    document.getElementById('pr-detail-labels-section').style.display = 'block';
                    var labelsHtml = data.labels.map(function(label) {
                        return '<span class="pr-label">' + escapeHtml(label) + '</span>';
                    }).join(' ');
                    document.getElementById('pr-detail-labels').innerHTML = labelsHtml;
                }

                // Checks
                if (data.checks && data.checks.length > 0) {
                    document.getElementById('pr-detail-checks-section').style.display = 'block';
                    var checksHtml = data.checks.map(function(check) {
                        var checkClass = 'pr-check';
                        if (check.toLowerCase().includes('success')) checkClass += ' success';
                        else if (check.toLowerCase().includes('failure')) checkClass += ' failure';
                        else if (check.toLowerCase().includes('pending') || check.toLowerCase().includes('in_progress')) checkClass += ' pending';
                        return '<span class="' + checkClass + '">' + escapeHtml(check) + '</span>';
                    }).join('');
                    document.getElementById('pr-detail-checks').innerHTML = checksHtml;
                }
            })
            .catch(function(err) {
                document.getElementById('pr-detail-title-text').textContent = 'Error';
                document.getElementById('pr-detail-body').textContent = 'Failed to load PR: ' + err.message;
            });
    }

    // Back button from PR detail
    var prBackBtn = document.getElementById('pr-back-btn');
    if (prBackBtn) {
        prBackBtn.addEventListener('click', function() {
            prDetail.style.display = 'none';
            prList.style.display = 'block';
            currentPrUrl = null;
            // Resume HTMX refresh
            window.pauseRefresh = false;
        });
    }

    // ============================================
    // SLING BUTTONS
    // ============================================
    var activeSlingDropdown = null;

    function closeSlingDropdown() {
        if (activeSlingDropdown) {
            activeSlingDropdown.remove();
            activeSlingDropdown = null;
        }
    }

    function openSlingDropdown(btn) {
        closeSlingDropdown();

        var beadId = btn.getAttribute('data-bead-id');
        if (!beadId) return;

        var dropdown = document.createElement('div');
        dropdown.className = 'sling-dropdown';
        dropdown.innerHTML = '<div class="sling-dropdown-loading">Loading rigs...</div>';

        // Position dropdown below the button
        var rect = btn.getBoundingClientRect();
        dropdown.style.position = 'fixed';
        dropdown.style.top = (rect.bottom + 4) + 'px';
        dropdown.style.left = rect.left + 'px';
        dropdown.style.zIndex = '10001';
        document.body.appendChild(dropdown);
        activeSlingDropdown = dropdown;

        // Fetch rig options
        fetch('/api/options?type=rigs')
            .then(function(r) { return r.json(); })
            .then(function(data) {
                var rigs = data.rigs || [];
                if (rigs.length === 0) {
                    dropdown.innerHTML = '<div class="sling-dropdown-empty">No rigs available</div>';
                    return;
                }
                var html = '<div class="sling-dropdown-header">Sling ' + escapeHtml(beadId) + ' to:</div>';
                for (var i = 0; i < rigs.length; i++) {
                    html += '<button class="sling-dropdown-item" data-rig="' + escapeHtml(rigs[i]) + '">' + escapeHtml(rigs[i]) + '</button>';
                }
                dropdown.innerHTML = html;

                // Handle rig selection
                dropdown.addEventListener('click', function(e) {
                    var item = e.target.closest('.sling-dropdown-item');
                    if (!item) return;
                    var rig = item.getAttribute('data-rig');
                    closeSlingDropdown();
                    executeSling(beadId, rig);
                });
            })
            .catch(function() {
                dropdown.innerHTML = '<div class="sling-dropdown-empty">Failed to load rigs</div>';
            });
    }

    function executeSling(beadId, rig) {
        var cmd = 'sling ' + beadId + ' ' + rig;
        showToast('info', 'Slinging...', beadId + ' → ' + rig);

        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: cmd, confirmed: true, timeout: 120 })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Slung', beadId + ' → ' + rig);
                if (data.output && data.output.trim()) {
                    showOutput(cmd, data.output);
                }
            } else {
                showToast('error', 'Sling failed', data.error || 'Unknown error');
                if (data.output) {
                    showOutput(cmd, data.output);
                }
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message || 'Request failed');
        });
    }

    // Click handler for sling buttons
    document.addEventListener('click', function(e) {
        var slingBtn = e.target.closest('.sling-btn');
        if (slingBtn) {
            e.preventDefault();
            e.stopPropagation();
            openSlingDropdown(slingBtn);
            return;
        }
        // Close dropdown when clicking outside
        if (activeSlingDropdown && !e.target.closest('.sling-dropdown')) {
            closeSlingDropdown();
        }
    });



    // ============================================
    // ESCALATION ACTIONS
    // ============================================
    document.addEventListener('click', function(e) {
        var btn = e.target.closest('.esc-btn');
        if (!btn) return;

        e.preventDefault();
        e.stopPropagation();

        var action = btn.getAttribute('data-action');
        var id = btn.getAttribute('data-id');
        if (!action || !id) return;

        if (action === 'reassign') {
            showReassignPicker(btn, id);
            return;
        }

        // Ack or Resolve - run directly
        var cmdName = 'escalate ' + action + ' ' + id;
        btn.disabled = true;
        btn.textContent = action === 'ack' ? 'Acking...' : 'Resolving...';

        runEscalationAction(cmdName, btn, action);
    });

    function runEscalationAction(cmdName, btn, action) {
        showToast('info', 'Running...', 'gt ' + cmdName);

        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: cmdName, confirmed: true })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.success) {
                showToast('success', 'Success', 'gt ' + cmdName);
                // Remove ack button or fade row on resolve
                var row = btn.closest('.escalation-row');
                if (action === 'resolve' && row) {
                    row.style.opacity = '0.4';
                    row.style.pointerEvents = 'none';
                } else if (action === 'ack' && row) {
                    // Replace ack button with ACK badge
                    btn.outerHTML = '<span class="badge badge-cyan">ACK</span>';
                }
            } else {
                showToast('error', 'Failed', data.error || 'Unknown error');
                btn.disabled = false;
                btn.textContent = action === 'ack' ? '👍 Ack' : '✓ Resolve';
            }
        })
        .catch(function(err) {
            showToast('error', 'Error', err.message || 'Request failed');
            btn.disabled = false;
            btn.textContent = action === 'ack' ? '👍 Ack' : '✓ Resolve';
        });
    }

    function showReassignPicker(btn, escalationId) {
        // Check if picker already open
        var existing = btn.parentNode.querySelector('.reassign-picker');
        if (existing) {
            existing.remove();
            return;
        }

        var picker = document.createElement('div');
        picker.className = 'reassign-picker';
        picker.innerHTML = '<select class="reassign-select"><option value="">Loading...</option></select>' +
            '<button class="esc-btn esc-reassign-confirm">Go</button>' +
            '<button class="esc-btn esc-reassign-cancel">✕</button>';
        btn.parentNode.appendChild(picker);

        var select = picker.querySelector('.reassign-select');

        // Pause refresh while picker is open
        window.pauseRefresh = true;

        // Load agents
        fetch('/api/options')
            .then(function(r) { return r.json(); })
            .then(function(data) {
                select.innerHTML = '<option value="">Select agent...</option>';
                var agents = data.agents || [];
                agents.forEach(function(agent) {
                    var name = typeof agent === 'string' ? agent : agent.name;
                    var running = typeof agent === 'object' ? agent.running : true;
                    var opt = document.createElement('option');
                    opt.value = name;
                    opt.textContent = name + (running ? '' : ' (stopped)');
                    select.appendChild(opt);
                });
            })
            .catch(function() {
                select.innerHTML = '<option value="">Failed to load</option>';
            });

        // Confirm reassign
        picker.querySelector('.esc-reassign-confirm').addEventListener('click', function() {
            var agent = select.value;
            if (!agent) {
                showToast('error', 'Missing', 'Select an agent to reassign to');
                return;
            }
            picker.remove();
            window.pauseRefresh = false;

            var cmdName = 'escalate reassign ' + escalationId + ' ' + agent;
            btn.disabled = true;
            btn.textContent = 'Reassigning...';

            showToast('info', 'Running...', 'gt ' + cmdName);

            fetch('/api/run', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ command: cmdName, confirmed: true })
            })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                if (data.success) {
                    showToast('success', 'Reassigned', 'Escalation reassigned to ' + agent);
                    var row = btn.closest('.escalation-row');
                    if (row) {
                        // Update the "From" cell to show new assignee
                        var fromCell = row.querySelectorAll('td')[2];
                        if (fromCell) fromCell.textContent = '→ ' + agent;
                    }
                } else {
                    showToast('error', 'Failed', data.error || 'Unknown error');
                }
                btn.disabled = false;
                btn.textContent = '↻ Reassign';
            })
            .catch(function(err) {
                showToast('error', 'Error', err.message || 'Request failed');
                btn.disabled = false;
                btn.textContent = '↻ Reassign';
            });
        });

        // Cancel
        picker.querySelector('.esc-reassign-cancel').addEventListener('click', function() {
            picker.remove();
            window.pauseRefresh = false;
        });
    }



    // ============================================
    // ACTIVITY TIMELINE FILTERS
    // ============================================

    function initTimelineFilters() {
        var timeline = document.getElementById('activity-timeline');
        if (!timeline) return;

        var entries = timeline.querySelectorAll('.tl-entry');
        var rigFilter = document.getElementById('tl-rig-filter');
        var agentFilter = document.getElementById('tl-agent-filter');
        var emptyMsg = document.getElementById('tl-empty-filtered');

        // Collect unique rigs and agents for dropdowns
        var rigs = {};
        var agents = {};
        entries.forEach(function(entry) {
            var rig = entry.getAttribute('data-rig');
            var agent = entry.getAttribute('data-agent');
            if (rig) rigs[rig] = true;
            if (agent) agents[agent] = true;
        });

        // Populate rig dropdown
        if (rigFilter) {
            Object.keys(rigs).sort().forEach(function(rig) {
                var opt = document.createElement('option');
                opt.value = rig;
                opt.textContent = rig;
                rigFilter.appendChild(opt);
            });
        }

        // Populate agent dropdown
        if (agentFilter) {
            Object.keys(agents).sort().forEach(function(agent) {
                var opt = document.createElement('option');
                opt.value = agent;
                opt.textContent = agent;
                agentFilter.appendChild(opt);
            });
        }

        // Current filter state
        var activeCategory = 'all';

        function applyFilters() {
            var selectedRig = rigFilter ? rigFilter.value : 'all';
            var selectedAgent = agentFilter ? agentFilter.value : 'all';
            var visibleCount = 0;

            entries.forEach(function(entry) {
                var show = true;

                if (activeCategory !== 'all' && entry.getAttribute('data-category') !== activeCategory) {
                    show = false;
                }
                if (selectedRig !== 'all' && entry.getAttribute('data-rig') !== selectedRig) {
                    show = false;
                }
                if (selectedAgent !== 'all' && entry.getAttribute('data-agent') !== selectedAgent) {
                    show = false;
                }

                if (show) {
                    entry.classList.remove('tl-hidden');
                    visibleCount++;
                } else {
                    entry.classList.add('tl-hidden');
                }
            });

            if (emptyMsg) {
                emptyMsg.style.display = visibleCount === 0 ? 'block' : 'none';
            }
        }

        // Category filter buttons
        document.addEventListener('click', function(e) {
            var btn = e.target.closest('.tl-filter-btn');
            if (!btn) return;
            if (btn.getAttribute('data-filter') !== 'category') return;

            // Update active state
            var group = btn.closest('.tl-filter-group');
            if (group) {
                group.querySelectorAll('.tl-filter-btn').forEach(function(b) {
                    b.classList.remove('active');
                });
            }
            btn.classList.add('active');
            activeCategory = btn.getAttribute('data-value');
            applyFilters();
        });

        // Dropdown filters
        if (rigFilter) {
            rigFilter.addEventListener('change', applyFilters);
        }
        if (agentFilter) {
            agentFilter.addEventListener('change', applyFilters);
        }
    }

    // Init on page load
    initTimelineFilters();

    // Re-init after HTMX swaps
    document.body.addEventListener('htmx:afterSwap', function() {
        initTimelineFilters();
    });

    // ============================================
    // SESSION TERMINAL PREVIEW
    // ============================================
    var sessionPreviewInterval = null;
    var sessionsTable = null; // will be set when opening preview

    // Click on session row to preview terminal output
    document.addEventListener('click', function(e) {
        var sessionRow = e.target.closest('.session-row');
        if (sessionRow) {
            e.preventDefault();
            var sessionName = sessionRow.getAttribute('data-session-name');
            if (sessionName) {
                openSessionPreview(sessionName);
            }
        }
    });

    function openSessionPreview(sessionName) {
        window.pauseRefresh = true;

        var preview = document.getElementById('session-preview');
        var nameEl = document.getElementById('session-preview-name');
        var contentEl = document.getElementById('session-preview-content');
        var statusEl = document.getElementById('session-preview-status');

        if (!preview || !contentEl) return;

        // Hide the sessions table, show preview
        sessionsTable = preview.parentNode.querySelector('table');
        if (sessionsTable) sessionsTable.style.display = 'none';
        var emptyState = preview.parentNode.querySelector('.empty-state');
        if (emptyState) emptyState.style.display = 'none';

        nameEl.textContent = sessionName;
        contentEl.textContent = 'Loading...';
        statusEl.textContent = '';
        preview.style.display = 'block';

        // Fetch immediately
        fetchSessionPreview(sessionName, contentEl, statusEl);

        // Auto-refresh every 3 seconds
        if (sessionPreviewInterval) clearInterval(sessionPreviewInterval);
        sessionPreviewInterval = setInterval(function() {
            fetchSessionPreview(sessionName, contentEl, statusEl);
        }, 3000);
    }

    function fetchSessionPreview(sessionName, contentEl, statusEl) {
        fetch('/api/session/preview?session=' + encodeURIComponent(sessionName))
            .then(function(r) { return r.json(); })
            .then(function(data) {
                if (data.error) {
                    contentEl.textContent = 'Error: ' + data.error;
                    return;
                }
                contentEl.textContent = data.content || '(empty)';
                // Auto-scroll to bottom
                contentEl.scrollTop = contentEl.scrollHeight;
                // Show refresh timestamp
                var now = new Date();
                var timeStr = now.getHours() + ':' + (now.getMinutes() < 10 ? '0' : '') + now.getMinutes() + ':' + (now.getSeconds() < 10 ? '0' : '') + now.getSeconds();
                statusEl.textContent = 'refreshed ' + timeStr;
            })
            .catch(function(err) {
                contentEl.textContent = 'Failed to load preview: ' + err.message;
            });
    }

    function closeSessionPreview() {
        if (sessionPreviewInterval) {
            clearInterval(sessionPreviewInterval);
            sessionPreviewInterval = null;
        }

        var preview = document.getElementById('session-preview');
        if (preview) preview.style.display = 'none';

        // Show the sessions table again
        if (sessionsTable) sessionsTable.style.display = '';

        window.pauseRefresh = false;
    }

    // Back button from session preview
    var sessionPreviewBack = document.getElementById('session-preview-back');
    if (sessionPreviewBack) {
        sessionPreviewBack.addEventListener('click', closeSessionPreview);
    }

    // ============================================
    // CONVOY DRILL-DOWN (expand rows to show tracked issues)
    // ============================================
    var convoyCache = {}; // Cache fetched convoy data by ID

    document.addEventListener('click', function(e) {
        var row = e.target.closest('.convoy-row');
        if (!row) return;

        e.preventDefault();
        var convoyId = row.getAttribute('data-convoy-id');
        if (!convoyId) return;

        // Check if already expanded
        var existingDetail = row.nextElementSibling;
        if (existingDetail && existingDetail.classList.contains('convoy-detail-row')) {
            // Collapse: remove the detail row
            existingDetail.remove();
            row.classList.remove('convoy-expanded');
            var toggle = row.querySelector('.convoy-toggle');
            if (toggle) toggle.textContent = '▶';
            return;
        }

        // Collapse any other expanded convoy
        document.querySelectorAll('.convoy-detail-row').forEach(function(r) { r.remove(); });
        document.querySelectorAll('.convoy-row.convoy-expanded').forEach(function(r) {
            r.classList.remove('convoy-expanded');
            var t = r.querySelector('.convoy-toggle');
            if (t) t.textContent = '▶';
        });

        // Mark this row as expanded
        row.classList.add('convoy-expanded');
        var toggleEl = row.querySelector('.convoy-toggle');
        if (toggleEl) toggleEl.textContent = '▼';

        // Create detail row
        var detailRow = document.createElement('tr');
        detailRow.className = 'convoy-detail-row';
        var detailCell = document.createElement('td');
        detailCell.colSpan = 4;
        detailCell.innerHTML = '<div class="tracked-issues"><div class="tracked-issues-loading">Loading tracked issues...</div></div>';
        detailRow.appendChild(detailCell);
        row.parentNode.insertBefore(detailRow, row.nextSibling);

        // Check cache first
        if (convoyCache[convoyId]) {
            renderConvoyIssues(detailCell, convoyCache[convoyId]);
            return;
        }

        // Fetch via /api/run
        fetch('/api/run', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command: 'convoy status ' + convoyId + ' --json' })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (!data.success) {
                detailCell.innerHTML = '<div class="tracked-issues"><div class="tracked-issues-error">Failed to load: ' + escapeHtml(data.error || 'Unknown error') + '</div></div>';
                return;
            }
            try {
                var parsed = JSON.parse(data.output);
                convoyCache[convoyId] = parsed;
                renderConvoyIssues(detailCell, parsed);
            } catch (err) {
                detailCell.innerHTML = '<div class="tracked-issues"><div class="tracked-issues-error">Failed to parse response</div></div>';
            }
        })
        .catch(function(err) {
            detailCell.innerHTML = '<div class="tracked-issues"><div class="tracked-issues-error">Request failed: ' + escapeHtml(err.message) + '</div></div>';
        });
    });

    function renderConvoyIssues(cell, data) {
        var issues = data.tracked || [];
        if (issues.length === 0) {
            cell.innerHTML = '<div class="tracked-issues"><div class="tracked-issues-empty">No tracked issues</div></div>';
            return;
        }

        var html = '<div class="tracked-issues">';
        html += '<table class="tracked-issues-table">';
        html += '<thead><tr><th>Status</th><th>ID</th><th>Title</th><th>Assignee</th><th>Progress</th></tr></thead>';
        html += '<tbody>';

        for (var i = 0; i < issues.length; i++) {
            var issue = issues[i];

            // Status badge
            var statusBadge = '';
            switch (issue.status) {
                case 'closed':
                    statusBadge = '<span class="badge badge-green">Done</span>';
                    break;
                case 'in_progress':
                    statusBadge = '<span class="badge badge-yellow">In Progress</span>';
                    break;
                case 'hooked':
                    statusBadge = '<span class="badge badge-blue">Hooked</span>';
                    break;
                default:
                    statusBadge = '<span class="badge badge-muted">Open</span>';
            }

            // Assignee - extract short name
            var assignee = '—';
            if (issue.assignee) {
                var parts = issue.assignee.split('/');
                assignee = parts[parts.length - 1];
            }

            // Worker info as progress indicator
            var progress = '';
            if (issue.status === 'closed') {
                progress = '<span class="convoy-progress-done">✓</span>';
            } else if (issue.worker) {
                var workerName = issue.worker.split('/').pop();
                progress = '<span class="convoy-progress-active">@' + escapeHtml(workerName) + '</span>';
                if (issue.worker_age) {
                    progress += ' <span class="convoy-progress-age">' + escapeHtml(issue.worker_age) + '</span>';
                }
            }

            html += '<tr class="tracked-issue-row tracked-issue-' + escapeHtml(issue.status) + '">' +
                '<td>' + statusBadge + '</td>' +
                '<td><span class="issue-id">' + escapeHtml(issue.id) + '</span></td>' +
                '<td class="tracked-issue-title">' + escapeHtml(issue.title) + '</td>' +
                '<td class="tracked-issue-assignee">' + escapeHtml(assignee) + '</td>' +
                '<td class="tracked-issue-progress">' + progress + '</td>' +
                '</tr>';
        }

        html += '</tbody></table>';

        // Progress summary
        var completed = data.completed || 0;
        var total = data.total || issues.length;
        var pct = total > 0 ? Math.round((completed / total) * 100) : 0;
        html += '<div class="tracked-issues-summary">';
        html += '<div class="tracked-issues-progress-bar"><div class="tracked-issues-progress-fill" style="width: ' + pct + '%;"></div></div>';
        html += '<span class="tracked-issues-progress-text">' + completed + '/' + total + ' completed (' + pct + '%)</span>';
        html += '</div>';

        html += '</div>';
        cell.innerHTML = html;
    }

})();
