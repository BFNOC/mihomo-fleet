import { writeClipboard } from "../api.ts";
import { showMessage } from "../bridge.ts";

/**
 * Copies a proxy field and reports the outcome. The success text varies by call
 * site (which field was copied), so it is passed in rather than built here.
 */
export async function copyProxyValue(value: string, success: string | undefined): Promise<void> {
  try {
    await writeClipboard(value);
    showMessage(success || "");
  } catch (err) {
    console.warn("Unable to copy proxy value.", err);
    showMessage("复制失败，请检查浏览器剪贴板权限。", "error");
  }
}
