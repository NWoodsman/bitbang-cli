"""Browser-to-shell input regression test.

This intentionally exercises the complete browser path: xterm.js, the injected
WebSocket shim, bootstrap.js, WebRTC/SWSP, and the listener's PTY implementation.
"""

import os
import queue
import re
import subprocess
import threading
import time

import pytest
from playwright.sync_api import expect


TEST_SERVER = os.environ.get("BITBANG_TEST_SERVER", "test.bitba.ng")


@pytest.fixture(scope="module")
def shell_url():
    binary = os.environ.get("BITBANG_BIN")
    if not binary or not os.path.isfile(binary):
        pytest.skip("Set BITBANG_BIN to the bitbang executable under test")

    proc = subprocess.Popen(
        [binary, "serve", "shell", "-server", TEST_SERVER, "-ephemeral"],
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    lines = queue.Queue()

    def pump():
        for line in proc.stdout:
            lines.put(line)
        lines.put(None)

    threading.Thread(target=pump, daemon=True).start()
    url = None
    ready = False
    captured = []
    deadline = time.time() + 30
    while time.time() < deadline:
        try:
            line = lines.get(timeout=max(0.1, deadline - time.time()))
        except queue.Empty:
            break
        if line is None:
            break
        captured.append(line)
        match = re.search(r"URL:\s*(https://\S+)", line)
        if match:
            url = match.group(1)
        if re.search(r"\bReady\b", line):
            ready = True
        if url and ready:
            break

    if not (url and ready):
        proc.kill()
        pytest.fail("shell listener did not start:\n" + "".join(captured))

    yield url

    proc.terminate()
    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        proc.kill()


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
