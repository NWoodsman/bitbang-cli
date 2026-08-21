"""Browser-to-shell input regression test.

This intentionally exercises the complete browser path: xterm.js, the injected
WebSocket shim, bootstrap.js, WebRTC/SWSP, and the listener's PTY implementation.
"""

import pytest
from playwright.sync_api import expect


@pytest.fixture(scope="module")
def shell_url(listener, test_server):
    """A shell-only listener. Ephemeral: this test has no link table, so it
    runs on the identity's own code."""
    return listener("serve", "shell", "-server", test_server, "-ephemeral").url


def test_browser_keystrokes_reach_shell(shell_url, playwright):
    # Use pytest-playwright's session-scoped `playwright` fixture rather than
    # calling sync_playwright() here. That fixture owns the event loop for the
    # whole session, and the sync API refuses to start inside a running loop:
    # creating our own passes when this file runs alone, and fails once any
    # earlier test (test_post_body, test_proxy_page) has instantiated it.
    browser = playwright.chromium.launch(headless=True)
    page = browser.new_page()
    try:
        page.goto(shell_url, wait_until="domcontentloaded", timeout=30_000)
        terminal = page.frame_locator("#device-frame").locator("#terminal")
        textarea = terminal.locator("textarea.xterm-helper-textarea")
        expect(textarea).to_be_attached(timeout=30_000)
        textarea.focus()

        marker = "BITBANG_BROWSER_INPUT_OK"
        page.keyboard.type("echo " + marker)
        page.keyboard.press("Enter")
        page.keyboard.type("exit")
        page.keyboard.press("Enter")

        expect(terminal).to_contain_text(marker, timeout=10_000)
        expect(terminal).to_contain_text("[exit 0]", timeout=10_000)
    finally:
        browser.close()
