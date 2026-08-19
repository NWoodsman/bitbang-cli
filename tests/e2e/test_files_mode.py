"""Files-only mode in a browser.

`serve files` takes the single-cap fast path in buildServeHTTPHandler --
the file browser is served at the root rather than mounted under /files/
so its relative URLs resolve -- which no other test reaches.
"""

import os

import pytest


@pytest.fixture(scope='module')
def files_url(listener, test_server, tmp_path_factory):
    home = str(tmp_path_factory.mktemp('files-home'))
    shared = os.path.join(home, 'shared')
    os.makedirs(shared)
    with open(os.path.join(shared, 'hello.txt'), 'w') as f:
        f.write('contents of hello\n')
    return listener('serve', 'files', shared, '-server', test_server,
                    '-ephemeral', home=home).url


def test_file_browser_lists_the_share(files_url, browser_context):
    page = browser_context.new_page()
    page.goto(files_url, wait_until='networkidle')
    frame = page.frame_locator('#device-frame')
    frame.locator('body').wait_for(timeout=20000)
    assert 'hello.txt' in frame.locator('body').inner_text()
    page.close()


def test_file_contents_download(files_url, browser_context):
    """Fetch the file through the proxied UI rather than clicking it: the
    point is that the file stream reaches the browser, not that the anchor
    is wired up."""
    page = browser_context.new_page()
    page.goto(files_url, wait_until='networkidle')
    frame = page.frame_locator('#device-frame')
    frame.locator('body').wait_for(timeout=20000)
    body = frame.locator('body').evaluate(
        "async () => (await fetch('api/download?path=hello.txt')).text()"
    )
    assert 'contents of hello' in body
    page.close()
