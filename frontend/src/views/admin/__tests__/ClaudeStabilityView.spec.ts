import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";

import type { ClaudeGatewaySettings } from "@/api/admin/settings";
import ClaudeStabilityView from "../ClaudeStabilityView.vue";

const {
  getClaudeGatewaySettings,
  updateClaudeGatewaySettings,
  showError,
  showSuccess,
} = vi.hoisted(() => ({
  getClaudeGatewaySettings: vi.fn(),
  updateClaudeGatewaySettings: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}));

vi.mock("@/api", () => ({
  adminAPI: {
    settings: {
      getClaudeGatewaySettings,
      updateClaudeGatewaySettings,
    },
  },
}));

vi.mock("@/stores", () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
  }),
}));

const loadedSettings = {
  stability_protection_enabled: false,
  respect_retry_after: false,
  retry_jitter_enabled: false,
  traffic_smoothing_enabled: false,
  max_retry_attempts: 7,
  retry_base_delay_ms: 900,
  retry_max_delay_ms: 9000,
  retry_max_elapsed_seconds: 60,
  rate_limit_fallback_cooldown_seconds: 90,
  oauth_auth_cooldown_minutes: 45,
  protocol_mode: "legacy",
  enabled: true,
  force_cli_beta_for_apikey: true,
  enable_cc_mimic_headers_for_apikey: true,
  cc_version_override: "9.9.9",
  cc_version_pool: ["9.9.9"],
  os_arch_pool: [
    { os: "custom-os", arch: "custom-arch" },
    { os: "linux", arch: "arm64" },
  ],
  extra_beta_tokens: ["custom-beta"],
  cache_control_ttl_override: "45m",
  fingerprint_salt_override: "persisted-salt",
  enable_tls_fingerprint: true,
  tls_fingerprint_profile_id: 42,
  tls_profile_pool_size: 8,
  min_inter_request_delay_ms: 500,
  max_inter_request_delay_ms: 900,
  system_prompt_static_override: "persisted static system prompt",
} satisfies ClaudeGatewaySettings;

function mountView(): VueWrapper {
  return mount(ClaudeStabilityView, {
    global: {
      stubs: {
        AppLayout: { template: "<div><slot /></div>" },
      },
    },
  });
}

function findButton(wrapper: VueWrapper, text: string): VueWrapper {
  const button = wrapper.findAll("button").find((candidate) => candidate.text().includes(text));
  expect(button, `expected button containing: ${text}`).toBeDefined();
  return button!;
}

function isDisabled(button: VueWrapper): boolean {
  return (button.element as HTMLButtonElement).disabled;
}

describe("ClaudeStabilityView", () => {
  beforeEach(() => {
    getClaudeGatewaySettings.mockReset();
    updateClaudeGatewaySettings.mockReset();
    showError.mockReset();
    showSuccess.mockReset();
  });

  it("blocks reset and save after an initial load failure until retry succeeds", async () => {
    getClaudeGatewaySettings
      .mockRejectedValueOnce({ message: "gateway settings unavailable" })
      .mockResolvedValueOnce(structuredClone(loadedSettings));
    updateClaudeGatewaySettings.mockImplementation(async (settings) => settings);

    const wrapper = mountView();
    await flushPromises();

    expect(wrapper.get('[data-testid="claude-stability-load-error"]').text()).toContain(
      "gateway settings unavailable",
    );

    const resetButton = findButton(wrapper, "恢复推荐值");
    const saveButton = findButton(wrapper, "保存并立即生效");
    expect(isDisabled(resetButton)).toBe(true);
    expect(isDisabled(saveButton)).toBe(true);

    await resetButton.trigger("click");
    await saveButton.trigger("click");
    expect(updateClaudeGatewaySettings).not.toHaveBeenCalled();

    await findButton(wrapper, "重试加载").trigger("click");
    await flushPromises();

    expect(getClaudeGatewaySettings).toHaveBeenCalledTimes(2);
    expect(wrapper.find('[data-testid="claude-stability-load-error"]').exists()).toBe(false);
    expect(isDisabled(findButton(wrapper, "恢复推荐值"))).toBe(false);
    expect(isDisabled(findButton(wrapper, "保存并立即生效"))).toBe(false);
  });

  it("preserves hidden server settings when restoring visible recommended values", async () => {
    getClaudeGatewaySettings.mockResolvedValue(structuredClone(loadedSettings));
    updateClaudeGatewaySettings.mockImplementation(async (settings) => settings);

    const wrapper = mountView();
    await flushPromises();

    await findButton(wrapper, "恢复推荐值").trigger("click");
    await findButton(wrapper, "保存并立即生效").trigger("click");
    await flushPromises();

    expect(updateClaudeGatewaySettings).toHaveBeenCalledTimes(1);
    const payload = updateClaudeGatewaySettings.mock.calls[0][0] as ClaudeGatewaySettings;
    expect(payload.max_retry_attempts).toBe(3);
    expect(payload.protocol_mode).toBe("passthrough");
    expect(payload.os_arch_pool).toEqual(loadedSettings.os_arch_pool);
    expect(payload.cache_control_ttl_override).toBe(loadedSettings.cache_control_ttl_override);
    expect(payload.fingerprint_salt_override).toBe(loadedSettings.fingerprint_salt_override);
    expect(payload.system_prompt_static_override).toBe(loadedSettings.system_prompt_static_override);
  });
});
