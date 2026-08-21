<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl space-y-6">
      <div class="overflow-hidden rounded-2xl border border-emerald-200 bg-gradient-to-br from-emerald-50 via-white to-sky-50 shadow-sm dark:border-emerald-900/60 dark:from-emerald-950/30 dark:via-dark-800 dark:to-sky-950/20">
        <div class="flex flex-col gap-5 p-6 md:flex-row md:items-center md:justify-between">
          <div>
            <div class="mb-2 flex items-center gap-2">
              <span class="inline-flex rounded-full bg-emerald-100 px-2.5 py-1 text-xs font-semibold text-emerald-700 dark:bg-emerald-900/50 dark:text-emerald-300">默认开启</span>
              <span class="text-xs text-gray-500 dark:text-gray-400">全局 Anthropic / Bedrock 配置</span>
            </div>
            <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">Claude 稳定性与兼容性</h1>
            <p class="mt-2 max-w-3xl text-sm leading-6 text-gray-600 dark:text-gray-300">
              集中管理重试退避、Retry-After、账号冷却和请求平滑。账号登录与编辑页不再重复选择这些策略。
            </p>
          </div>
          <div class="flex flex-wrap gap-3">
            <button type="button" class="btn btn-secondary" :disabled="loading || saving || !hasLoaded" @click="resetRecommended">
              恢复推荐值
            </button>
            <button type="button" class="btn btn-primary" :disabled="loading || saving || !hasLoaded" @click="save">
              {{ saving ? "保存中..." : "保存并立即生效" }}
            </button>
          </div>
        </div>
      </div>

      <div v-if="loading" class="card flex min-h-48 items-center justify-center">
        <div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600"></div>
      </div>

      <div v-else-if="loadError" data-testid="claude-stability-load-error" role="alert" class="card flex min-h-48 flex-col items-center justify-center gap-4 px-6 py-10 text-center">
        <div>
          <h2 class="text-lg font-semibold text-gray-900 dark:text-white">Claude 稳定性设置加载失败</h2>
          <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">{{ loadError }}</p>
        </div>
        <button type="button" class="btn btn-primary" :disabled="loading || saving" @click="load">
          重试加载
        </button>
      </div>

      <template v-else-if="hasLoaded">
        <section class="card">
          <div class="flex flex-col gap-4 border-b border-gray-100 px-6 py-5 dark:border-dark-700 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <h2 class="text-lg font-semibold text-gray-900 dark:text-white">稳定性保护</h2>
              <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">开启后仅对瞬时错误执行有限重试，并对同一账号做短间隔流量平滑。</p>
            </div>
            <Toggle v-model="form.stability_protection_enabled" />
          </div>

          <div class="space-y-5 p-6" :class="!form.stability_protection_enabled && 'opacity-60'">
            <div class="grid gap-4 md:grid-cols-3">
              <SettingToggle v-model="form.respect_retry_after" title="遵循 Retry-After" description="优先采用上游给出的重试时间，避免过早重复请求。" />
              <SettingToggle v-model="form.retry_jitter_enabled" title="退避随机抖动" description="分散并发请求的重试时刻，减少同步重试峰值。" />
              <SettingToggle v-model="form.traffic_smoothing_enabled" title="账号流量平滑" description="按账号错开短时间内的请求，重试仍走独立退避。" />
            </div>

            <div class="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
              <NumberField v-model="form.max_retry_attempts" label="最大尝试次数" :min="1" :max="8" suffix="次（含首次）" />
              <NumberField v-model="form.retry_base_delay_ms" label="基础退避" :min="50" :max="30000" suffix="ms" />
              <NumberField v-model="form.retry_max_delay_ms" label="单次退避上限" :min="50" :max="120000" suffix="ms" />
              <NumberField v-model="form.retry_max_elapsed_seconds" label="总重试时间上限" :min="1" :max="300" suffix="秒" />
            </div>

            <div class="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
              <NumberField v-model="form.rate_limit_fallback_cooldown_seconds" label="429 无重置头冷却" :min="1" :max="7200" suffix="秒" />
              <NumberField v-model="form.oauth_auth_cooldown_minutes" label="OAuth 401 冷却" :min="1" :max="1440" suffix="分钟" />
              <NumberField v-model="form.min_inter_request_delay_ms" label="账号最小请求间隔" :min="0" :max="60000" suffix="ms" />
              <NumberField v-model="form.max_inter_request_delay_ms" label="账号最大请求间隔" :min="0" :max="60000" suffix="ms" />
            </div>

            <div class="rounded-lg border border-sky-200 bg-sky-50 p-4 text-sm leading-6 text-sky-800 dark:border-sky-900/60 dark:bg-sky-950/20 dark:text-sky-300">
              401/403 不会进入同凭据通用重试；429、408、409 与 5xx 才进入有限重试。超过时间预算后切换账号或进入冷却。
            </div>
          </div>
        </section>

        <section class="card">
          <div class="border-b border-gray-100 px-6 py-5 dark:border-dark-700">
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">协议兼容模式</h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">推荐保持原样透传。旧版合成只用于已经确认需要特定兼容行为的上游。</p>
          </div>
          <div class="space-y-5 p-6">
            <div class="grid gap-4 md:grid-cols-2">
              <label class="cursor-pointer rounded-xl border p-4 transition" :class="form.protocol_mode === 'passthrough' ? selectedModeClass : normalModeClass">
                <div class="flex items-start gap-3">
                  <input v-model="form.protocol_mode" type="radio" value="passthrough" class="mt-1" />
                  <div>
                    <div class="font-medium text-gray-900 dark:text-white">原样透传（推荐）</div>
                    <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">不主动合成 Claude Code 身份、Header 或 TLS Profile。</p>
                  </div>
                </div>
              </label>
              <label class="cursor-pointer rounded-xl border p-4 transition" :class="form.protocol_mode === 'legacy' ? warningModeClass : normalModeClass">
                <div class="flex items-start gap-3">
                  <input v-model="form.protocol_mode" type="radio" value="legacy" class="mt-1" />
                  <div>
                    <div class="font-medium text-gray-900 dark:text-white">旧版兼容合成</div>
                    <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">保留历史兼容路径；需要同时保持版本、Header、Beta 与 TLS 配置一致。</p>
                  </div>
                </div>
              </label>
            </div>

            <div v-if="form.protocol_mode === 'legacy'" class="space-y-5 rounded-xl border border-amber-200 bg-amber-50/70 p-5 dark:border-amber-900/60 dark:bg-amber-950/20">
              <div class="flex items-center justify-between gap-4">
                <div>
                  <h3 class="font-medium text-amber-900 dark:text-amber-200">启用 API Key 旧版合成</h3>
                  <p class="mt-1 text-sm text-amber-700 dark:text-amber-300">此开关关闭时，即使选择 legacy 也不会给 API Key 请求注入兼容身份。</p>
                </div>
                <Toggle v-model="form.enabled" />
              </div>

              <div class="grid gap-4 md:grid-cols-2">
                <SettingToggle v-model="form.enable_cc_mimic_headers_for_apikey" title="注入 Claude Code Headers" description="写入旧版 User-Agent 与 x-stainless 兼容头。" />
                <SettingToggle v-model="form.force_cli_beta_for_apikey" title="补充 CLI Beta Headers" description="客户端未提供时补充旧版 Beta token 集合。" />
                <SettingToggle v-model="form.enable_tls_fingerprint" title="启用全局 TLS Profile" description="由全局 Profile 替代历史单账号选择。" />
                <NumberField v-model="form.tls_fingerprint_profile_id" label="TLS Profile ID" :min="0" :max="2147483647" suffix="0 = 内置" />
                <NumberField v-model="form.tls_profile_pool_size" label="TLS Profile 池大小" :min="0" :max="32" suffix="个" />
                <div>
                  <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">固定 Claude Code 版本</label>
                  <input v-model.trim="form.cc_version_override" class="input w-full" placeholder="留空使用同步版本" />
                </div>
              </div>

              <div class="grid gap-4 md:grid-cols-2">
                <div>
                  <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">版本池（每行一个）</label>
                  <textarea v-model="versionPoolDraft" rows="4" class="input w-full font-mono text-xs" placeholder="2.x.x" />
                </div>
                <div>
                  <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">额外 Beta Tokens（每行一个）</label>
                  <textarea v-model="betaTokensDraft" rows="4" class="input w-full font-mono text-xs" />
                </div>
              </div>
            </div>
          </div>
        </section>

        <div class="flex justify-end gap-3 pb-8">
          <button type="button" class="btn btn-secondary" :disabled="saving" @click="load">重新加载</button>
          <button type="button" class="btn btn-primary" :disabled="loading || saving || !hasLoaded" @click="save">
            {{ saving ? "保存中..." : "保存并立即生效" }}
          </button>
        </div>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, onMounted, reactive, ref } from "vue";
import { adminAPI } from "@/api";
import type { ClaudeGatewaySettings } from "@/api/admin/settings";
import AppLayout from "@/components/layout/AppLayout.vue";
import Toggle from "@/components/common/Toggle.vue";
import { useAppStore } from "@/stores";
import { extractApiErrorMessage } from "@/utils/apiError";

const appStore = useAppStore();
const loading = ref(true);
const saving = ref(false);
const hasLoaded = ref(false);
const loadError = ref("");
const versionPoolDraft = ref("");
const betaTokensDraft = ref("");

const recommendedDefaults = (): ClaudeGatewaySettings => ({
  stability_protection_enabled: true,
  respect_retry_after: true,
  retry_jitter_enabled: true,
  traffic_smoothing_enabled: true,
  max_retry_attempts: 3,
  retry_base_delay_ms: 500,
  retry_max_delay_ms: 5000,
  retry_max_elapsed_seconds: 15,
  rate_limit_fallback_cooldown_seconds: 30,
  oauth_auth_cooldown_minutes: 30,
  protocol_mode: "passthrough",
  enabled: false,
  force_cli_beta_for_apikey: false,
  enable_cc_mimic_headers_for_apikey: false,
  cc_version_override: "",
  cc_version_pool: [],
  os_arch_pool: [],
  extra_beta_tokens: [],
  cache_control_ttl_override: "",
  fingerprint_salt_override: "",
  enable_tls_fingerprint: false,
  tls_fingerprint_profile_id: 0,
  tls_profile_pool_size: 0,
  min_inter_request_delay_ms: 120,
  max_inter_request_delay_ms: 360,
  system_prompt_static_override: "",
});

type HiddenClaudeGatewaySettings = Pick<
  ClaudeGatewaySettings,
  "os_arch_pool" | "cache_control_ttl_override" | "fingerprint_salt_override" | "system_prompt_static_override"
>;

const form = reactive<ClaudeGatewaySettings>(recommendedDefaults());
const loadedHiddenSettings = ref<HiddenClaudeGatewaySettings | null>(null);
const selectedModeClass = "border-emerald-400 bg-emerald-50 ring-1 ring-emerald-300 dark:border-emerald-700 dark:bg-emerald-950/20";
const warningModeClass = "border-amber-400 bg-amber-50 ring-1 ring-amber-300 dark:border-amber-700 dark:bg-amber-950/20";
const normalModeClass = "border-gray-200 bg-white hover:border-gray-300 dark:border-dark-700 dark:bg-dark-800";

const SettingToggle = defineComponent({
  name: "SettingToggle",
  props: {
    modelValue: { type: Boolean, required: true },
    title: { type: String, required: true },
    description: { type: String, required: true },
  },
  emits: ["update:modelValue"],
  setup(props, { emit }) {
    return () => h("div", { class: "flex items-center justify-between gap-4 rounded-xl border border-gray-200 p-4 dark:border-dark-700" }, [
      h("div", null, [
        h("div", { class: "text-sm font-medium text-gray-900 dark:text-white" }, props.title),
        h("p", { class: "mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400" }, props.description),
      ]),
      h(Toggle, { modelValue: props.modelValue, "onUpdate:modelValue": (value: boolean) => emit("update:modelValue", value) }),
    ]);
  },
});

const NumberField = defineComponent({
  name: "NumberField",
  props: {
    modelValue: { type: Number, required: true },
    label: { type: String, required: true },
    min: { type: Number, required: true },
    max: { type: Number, required: true },
    suffix: { type: String, required: true },
  },
  emits: ["update:modelValue"],
  setup(props, { emit }) {
    const value = computed({
      get: () => props.modelValue,
      set: (next: number | string) => emit("update:modelValue", Number(next)),
    });
    return () => h("label", { class: "block" }, [
      h("span", { class: "mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300" }, props.label),
      h("div", { class: "relative" }, [
        h("input", {
          class: "input w-full pr-24",
          type: "number",
          min: props.min,
          max: props.max,
          value: value.value,
          onInput: (event: Event) => { value.value = (event.target as HTMLInputElement).value; },
        }),
        h("span", { class: "pointer-events-none absolute inset-y-0 right-3 flex items-center text-xs text-gray-400" }, props.suffix),
      ]),
    ]);
  },
});

function splitLines(raw: string): string[] {
  return [...new Set(raw.split(/[\n,]/).map((item) => item.trim()).filter(Boolean))];
}

function syncDrafts(): void {
  versionPoolDraft.value = (form.cc_version_pool || []).join("\n");
  betaTokensDraft.value = (form.extra_beta_tokens || []).join("\n");
}

function getHiddenSettings(settings: HiddenClaudeGatewaySettings): HiddenClaudeGatewaySettings {
  return {
    os_arch_pool: settings.os_arch_pool.map((entry) => ({ ...entry })),
    cache_control_ttl_override: settings.cache_control_ttl_override,
    fingerprint_salt_override: settings.fingerprint_salt_override,
    system_prompt_static_override: settings.system_prompt_static_override,
  };
}

function resetRecommended(): void {
  if (!hasLoaded.value || !loadedHiddenSettings.value) return;

  Object.assign(form, recommendedDefaults(), getHiddenSettings(loadedHiddenSettings.value));
  syncDrafts();
  appStore.showSuccess("已恢复推荐值，点击保存后生效");
}

async function load(): Promise<void> {
  loading.value = true;
  hasLoaded.value = false;
  loadError.value = "";
  try {
    const settings = await adminAPI.settings.getClaudeGatewaySettings();
    Object.assign(form, recommendedDefaults(), settings);
    loadedHiddenSettings.value = getHiddenSettings(settings);
    syncDrafts();
    hasLoaded.value = true;
  } catch (error: unknown) {
    const message = extractApiErrorMessage(error, "加载 Claude 稳定性设置失败");
    loadError.value = message;
    appStore.showError(message);
  } finally {
    loading.value = false;
  }
}

async function save(): Promise<void> {
  if (!hasLoaded.value || loading.value || saving.value) return;

  saving.value = true;
  try {
    form.cc_version_pool = splitLines(versionPoolDraft.value);
    form.extra_beta_tokens = splitLines(betaTokensDraft.value);
    const updated = await adminAPI.settings.updateClaudeGatewaySettings({ ...form });
    Object.assign(form, updated);
    loadedHiddenSettings.value = getHiddenSettings(updated);
    syncDrafts();
    appStore.showSuccess("Claude 稳定性设置已保存并立即生效");
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, "保存 Claude 稳定性设置失败"));
  } finally {
    saving.value = false;
  }
}

onMounted(load);
</script>
