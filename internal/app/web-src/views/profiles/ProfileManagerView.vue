<script setup lang="ts">
// #profilePanel's content: the two-column profile catalog + editor pane.
//
// This file is layout plus the config-editor toolbar. The pieces it composes:
//   profile-context.ts     form fields, dirty state, the operation-context guard
//   config-editor.ts       the CodeMirror handle and everything sequencing it
//   profile-navigation.ts  startNew/select/open/close (+ the two bridge actions)
//   profile-operations.ts  save/delete/refresh
//
// YamlCodeEditor stays a direct child of this template on purpose -- see
// config-editor.ts for why putting a component between them would turn every
// editor call into a silent no-op.
import { computed } from "vue";
import { store } from "../../store.ts";
import { chrome } from "../../bridge.ts";
import YamlCodeEditor from "./YamlCodeEditor.vue";
import ProfileCatalog from "./ProfileCatalog.vue";
import ProfileFormFields from "./ProfileFormFields.vue";
import ProfileReferenceList from "./ProfileReferenceList.vue";
import { activeProfile, hasEditor, isSubscription, references } from "./profile-context.ts";
import {
  configEditorErrorText,
  configEditorStatus,
  configContextMatches,
  discardConfig,
  editorLoadErrorText,
  editorRef,
  onEditorChange,
  onEditorLoadError,
  onEditorReady,
} from "./config-editor.ts";
import { startNewProfile } from "./profile-navigation.ts";
import { deleteProfile, saveProfile } from "./profile-operations.ts";

// `activeProfile`, not `store.activeProfileId`: the id can point at a profile
// that is no longer in the list (transiently, mid-delete), and both buttons must
// be disabled in that window too.
const noProfileSelected = computed(() => !activeProfile.value && !store.profileCreating);

const saveDisabled = computed(() =>
  noProfileSelected.value
  || (!isSubscription.value && !configContextMatches.value)
  || chrome.profileBusy,
);
const findDisabled = computed(() => noProfileSelected.value || isSubscription.value || chrome.profileBusy);
const discardDisabled = computed(() => !store.profileConfigDirty || chrome.profileBusy);
const deleteDisabled = computed(() => store.profileCreating || references.value > 0 || chrome.profileBusy);

const deleteHintText = computed(() =>
  store.profileCreating
    ? ""
    : (references.value > 0 ? `该配置档仍被 ${references.value} 个实例引用，需先将这些实例改绑到其他配置档。` : "删除后无法恢复。"),
);
</script>

<template>
  <div class="profile-manager-head">
    <div class="panel-title">
      <div>
        <h2 id="profileManagerTitle">配置档管理</h2>
        <p>配置档保存共享 YAML 或订阅，多个实例可以同时引用同一份配置。</p>
      </div>
    </div>
    <button id="newProfileBtn" class="primary" type="button" :disabled="chrome.profileBusy" @click="startNewProfile">新建配置档</button>
  </div>
  <div class="profile-manager-grid">
    <ProfileCatalog />
    <div class="profile-editor-pane">
      <div id="profileEditorEmpty" class="profile-editor-empty" :class="{ hidden: hasEditor }">
        <h3>选择配置档</h3>
        <p>从左侧选择已有配置档，或新建一份手写配置或订阅配置。</p>
      </div>
      <!--
        Always mounted, `.hidden`-toggled rather than v-if -- required so the
        CodeMirror host further down never sits behind a conditionally rendered
        ancestor (see YamlCodeEditor.vue's header comment).
      -->
      <section id="profileEditor" :class="{ hidden: !hasEditor }" aria-labelledby="profileEditorTitle">
        <ProfileFormFields />
        <div id="profileConfigSection" :class="{ hidden: store.profileCreating && isSubscription }">
          <div class="config-editor-toolbar">
            <div class="config-editor-heading">
              <span id="configEditorLabel">YAML 配置</span>
              <span id="configEditorStatus" class="config-editor-status" role="status" aria-live="polite" :data-state="configEditorStatus.state">{{ configEditorStatus.text }}</span>
            </div>
            <div class="actions" role="toolbar" aria-label="配置编辑操作">
              <button id="findConfig" type="button" :disabled="findDisabled" @click="editorRef?.focusSearch()">查找</button>
              <button id="discardConfig" type="button" :disabled="discardDisabled" @click="discardConfig">放弃修改</button>
            </div>
          </div>
          <YamlCodeEditor
            ref="editorRef"
            :active="store.view === 'profiles'"
            @change="onEditorChange"
            @save="saveProfile"
            @ready="onEditorReady"
            @load-error="onEditorLoadError"
          />
          <div id="configEditorError" class="config-editor-error" :class="{ hidden: !configEditorErrorText && !editorLoadErrorText }" role="alert">{{ editorLoadErrorText || configEditorErrorText }}</div>
        </div>
        <p id="profileDeleteHint" class="profile-delete-hint">{{ deleteHintText }}</p>
        <ProfileReferenceList v-if="references > 0" />
        <div class="profile-editor-actions">
          <button id="saveProfile" class="primary" type="button" :disabled="saveDisabled" @click="saveProfile">保存配置档</button>
          <button id="deleteProfile" class="danger" type="button" :class="{ hidden: store.profileCreating }" :disabled="deleteDisabled" @click="deleteProfile">删除配置档</button>
        </div>
      </section>
    </div>
  </div>
</template>
