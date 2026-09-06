const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../static/embed.js'), 'utf8');

function dashboard({ search = '?embed=1', origin = 'https://canvas.example', framed = true } = {}) {
    const messages = [];
    const window = { location: { search }, parent: { postMessage: (data, target) => messages.push({ data, target }) } };
    if (!framed) window.parent = window;
    const document = { querySelector: () => origin === null ? null : { content: origin } };
    vm.runInNewContext(source, { window, document, URL, URLSearchParams });
    return { focus: window.dashboardFocusBead, messages };
}

test('sends exact v1 event with actual HQ and cross-rig IDs to configured origin', () => {
    const { focus, messages } = dashboard({ search: '?embed=1&parentOrigin=https://evil.example' });
    for (const id of ['hq-xji2', 'inktree-3r67i', 'inktree-d2rn2.6', 'gt-or0']) assert.equal(focus(id), true);
    assert.deepEqual(JSON.parse(JSON.stringify(messages.slice(1))), ['hq-xji2', 'inktree-3r67i', 'inktree-d2rn2.6', 'gt-or0'].map(beadId => ({
        data: { type: 'gastown:focus-bead', version: 1, beadId }, target: 'https://canvas.example'
    })));
});

test('announces readiness once with no bead, URL or token', () => {
    const { messages } = dashboard({ search: '?embed=1&parentOrigin=https://evil.example' });
    assert.deepEqual(JSON.parse(JSON.stringify(messages)), [{
        data: { type: 'gastown:ready', version: 1 }, target: 'https://canvas.example'
    }]);
});

test('standalone and unconfigured frames retain local navigation', () => {
    for (const options of [{ search: '' }, { search: '?embed=0' }, { framed: false }, { origin: null }]) {
        const { focus, messages } = dashboard(options);
        assert.equal(focus('gt-or0'), false);
        assert.equal(messages.length, 0);
    }
});

test('cross-rig dependency wrappers emit the actual bead identity', () => {
    const { focus, messages } = dashboard();
    assert.equal(focus('external:inktree:inktree-d2rn2.6'), true);
    assert.equal(messages[1].data.beadId, 'inktree-d2rn2.6');
});

test('malformed origins and IDs cannot send events', () => {
    for (const origin of ['*', 'null', 'https://canvas.example/path', 'javascript:alert(1)', 'https://user@canvas.example']) {
        const { focus, messages } = dashboard({ origin });
        assert.equal(focus('gt-or0'), false);
        assert.equal(messages.length, 0);
    }
    const { focus, messages } = dashboard();
    for (const id of [null, {}, '', 'http://evil.example', 'external:inktree', 'external:inktree:gt-abc:extra', 'gt-abc --close', 'gt-<script>', 'gt-' + 'x'.repeat(256)]) assert.equal(focus(id), false);
    assert.equal(messages.length, 1); // only the bootstrap ready event
});
