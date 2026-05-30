from playwright.sync_api import expect, sync_playwright


def main():
    with sync_playwright() as p:
        browser = p.chromium.launch(
            headless=True,
            executable_path="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
        )
        page = browser.new_page(viewport={"width": 1366, "height": 820})
        page.goto("http://127.0.0.1:47891")
        page.wait_for_load_state("networkidle")

        expect(page.locator("h1")).to_have_text("Mihomo Fleet")
        expect(page.locator("#systemWarning")).to_contain_text("mihomo was not found")
        expect(page.locator("#emptyPanel")).to_contain_text("No instances yet")

        page.get_by_role("button", name="Create first instance").click()
        page.locator("#createName").fill("HK gateway")
        page.locator("#createSubmit").click()
        expect(page.locator("#detailName")).to_have_text("HK gateway")
        expect(page.locator("#metricStatus")).to_have_text("stopped")
        expect(page.locator("#metricMixed")).to_have_text("28000")
        expect(page.locator("#metricController")).to_have_text("29000")

        page.locator("#startBtn").click()
        expect(page.locator("#message")).to_contain_text("mihomo binary not found")
        expect(page.locator("#metricStatus")).to_have_text("error")

        browser.close()


if __name__ == "__main__":
    main()
