(function() {
    'use strict';

    // One-way navigation only. No incoming messages, commands, URLs or tokens.
    window.dashboardFocusBead = function(beadId) {
        var meta = document.querySelector('meta[name="dashboard-parent-origin"]');
        if (window.parent === window || new URLSearchParams(window.location.search).get('embed') !== '1' || !meta) return false;
        if (typeof beadId !== 'string' || beadId.length > 256 || !/^[a-zA-Z0-9][a-zA-Z0-9._-]*-[a-zA-Z0-9._-]+$/.test(beadId)) return false;
        var origin = meta.content;
        try {
            var parsed = new URL(origin);
            if (parsed.origin !== origin || !/^https?:$/.test(parsed.protocol)) return false;
        } catch (_) {
            return false;
        }
        window.parent.postMessage({ type: 'gastown:focus-bead', version: 1, beadId: beadId }, origin);
        return true;
    };
})();
