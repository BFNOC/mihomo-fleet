<script setup lang="ts">
// Name/id, the create-source segmented control, and the subscription settings.
// Replaces the pre-Vue renderProfileManager()'s form half plus
// renderSubscriptionInfo().
//
// Binds the shared refs in profile-context.ts directly rather than taking props:
// the form fields and the operation guard that reads their owner id are one
// mechanism, and prop-drilling five v-models through here would only obscure
// that.
import { computed } from "vue";
import { store } from "../../store.ts";
import { chrome } from "../../bridge.ts";
import { formatProfileUpdate, formatSubscriptionInfo, isHttpUrl } from "../../format.ts";
import {
  activeProfile,
  isSubscription,
  markProfileFormDirty,
  profileIdDisplay,
  profileNameInput,
  profileNameInputRef,
  references,
  subscriptionAutoUpdateInput,
  subscriptionIntervalInput,
  subscriptionUrlInput,
} from "./profile-context.ts";
import { setProfileCreateSource } from "./profile-navigation.ts";
import { refreshSubscription } from "./profile-operations.ts";
import ProfileReferenceList from "./ProfileReferenceList.vue";

const profileMetaText = computed(() => {
  if (store.profileCreating) return isSubscription.value ? "创建后会下载并缓存订阅 YAML。" : "手写配置可以直接编辑 YAML。";
  const profile = activeProfile.value;
  if (!profile) return "未选择配置档。";
  return isSubscription.value ? `订阅缓存：${formatProfileUpdate(profile)}` : "手写配置：修改会作用于所有引用实例。";
});

const referenceBadgeText = computed(() =>
  store.profileCreating ? "尚未创建" : (references.value > 0 ? `${references.value} 个实例引用` : "未使用"),
);

const subscriptionInfoText = computed(() => {
  const profile = activeProfile.value;
  return isSubscription.value && profile ? formatSubscriptionInfo(profile) : "";
});

const subscriptionHomeUrl = computed(() => {
  const profile = activeProfile.value;
  if (!isSubscription.value || !profile) return "";
  const homeUrl = (profile.homeUrl || "").trim();
  return isHttpUrl(homeUrl) ? homeUrl : "";
});
</script>

<template>
  <div class="profile-editor-head">
    <div>
      <h3 id="profileEditorTitle">{{ store.profileCreating ? "新建配置档" : (activeProfile?.name || "") }}</h3>
      <p id="profileMeta">{{ profileMetaText }}</p>
    </div>
    <span id="profileReferenceBadge" class="reference-badge" :class="{ 'in-use': references > 0 }">{{ referenceBadgeText }}</span>
  </div>
  <ProfileReferenceList v-if="!store.profileCreating" />
  <div class="form-grid profile-basics">
    <label>
      <span>名称</span>
      <input id="profileName" ref="profileNameInputRef" v-model="profileNameInput" placeholder="我的订阅" :disabled="chrome.profileBusy" @input="markProfileFormDirty">
    </label>
    <label>
      <span>配置档 ID</span>
      <input id="profileId" :value="profileIdDisplay" readonly>
    </label>
  </div>
  <div id="profileSourceTabs" class="segmented" :class="{ hidden: !store.profileCreating }" role="group" aria-label="配置来源">
    <button id="profileManualMode" type="button" :class="{ active: store.profileCreateSource === 'manual' }" :disabled="chrome.profileBusy" @click="setProfileCreateSource('manual')">手写配置</button>
    <button id="profileSubscriptionMode" type="button" :class="{ active: store.profileCreateSource === 'subscription' }" :disabled="chrome.profileBusy" @click="setProfileCreateSource('subscription')">订阅链接</button>
  </div>
  <div id="subscriptionSettings" class="subscription-settings" :class="{ hidden: !isSubscription }">
    <label class="stacked">
      <span>订阅链接</span>
      <input id="subscriptionUrl" v-model="subscriptionUrlInput" placeholder="https://example.com/sub" :disabled="chrome.profileBusy" @input="markProfileFormDirty">
    </label>
    <div class="form-grid subscription-fields">
      <label>
        <span>更新间隔（分钟）</span>
        <input id="subscriptionInterval" v-model="subscriptionIntervalInput" type="number" min="15" placeholder="360" :disabled="chrome.profileBusy" @input="markProfileFormDirty" @change="markProfileFormDirty">
      </label>
      <label class="checkline">
        <input id="subscriptionAutoUpdate" v-model="subscriptionAutoUpdateInput" type="checkbox" :disabled="chrome.profileBusy" @change="markProfileFormDirty">
        <span>自动更新</span>
      </label>
      <div id="subscriptionInfo" class="subscription-info">
        <span>{{ subscriptionInfoText }}</span>
        <template v-if="subscriptionHomeUrl">
          <span> · 主页 </span>
          <a :href="subscriptionHomeUrl" target="_blank" rel="noopener noreferrer">{{ subscriptionHomeUrl }}</a>
        </template>
      </div>
    </div>
    <div class="actions">
      <button id="refreshSubscription" type="button" :disabled="store.profileCreating || store.profileFormDirty || chrome.profileBusy" @click="refreshSubscription">立即更新</button>
    </div>
  </div>
</template>
