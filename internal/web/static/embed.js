(function() {
    'use strict';

    // One-way navigation only. No incoming messages, commands, URLs or tokens.
    window.dashboardFocusBead = function(beadId) {
        var meta = document.querySelector('meta[name="dashboard-parent-origin"]');
        if (window.parent === window || new URLSearchParams(window.location.search).get('embed') !== '1' || !meta) return false;
        // Dependency APIs may preserve beads' external:prefix:id wrapper.
        // Canvas resolves the actual ID, just as the standalone issue API does.
        if (typeof beadId === 'string' && beadId.length <= 512) {
            var external = beadId.match(/^external:[a-zA-Z0-9._-]+:([^:]+)$/);
            if (external) beadId = external[1];
        }
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
