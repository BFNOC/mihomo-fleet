<script setup lang="ts">
// Left-hand profile list. Replaces the pre-Vue renderProfileList().
import { store } from "../../store.ts";
import { chrome } from "../../bridge.ts";
import { profileReferenceCount } from "../../state.ts";
import { selectProfile } from "./profile-navigation.ts";

function referenceLabel(profileId: string): string {
  const count = profileReferenceCount(store, profileId);
  return count > 0 ? `${count} 个实例` : "未使用";
}

function isCurrent(profileId: string): boolean {
  return !store.profileCreating && store.activeProfileId === profileId;
}
</script>

<template>
  <nav class="profile-catalog" aria-label="配置档列表">
    <div class="profile-catalog-head">
      <h3>配置档</h3>
      <span id="profileCount">{{ store.profiles.length }} 个</span>
    </div>
    <div id="profileList" class="profile-list">
      <button
        v-for="profile in store.profiles"
        :key="profile.id"
        type="button"
        class="profile-row"
        :class="{ active: isCurrent(profile.id) }"
        :disabled="chrome.profileBusy"
        :aria-current="isCurrent(profile.id) ? 'true' : 'false'"
        :data-profile-id="profile.id"
        @click="selectProfile(profile.id)"
      >
        <span class="profile-row-main">{{ profile.name || "未命名配置档" }}</span>
        <span class="profile-row-meta">{{ profile.subscriptionUrl ? "订阅配置" : "手写配置" }} · {{ referenceLabel(profile.id) }}</span>
        <code class="profile-row-id">{{ profile.id }}</code>
      </button>
      <p v-if="!store.profiles.length" class="profile-list-empty">还没有配置档。</p>
    </div>
  </nav>
</template>
