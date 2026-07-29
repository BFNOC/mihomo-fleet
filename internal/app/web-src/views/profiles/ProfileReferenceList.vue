<script setup lang="ts">
// Turns "N 个实例引用" from a dead-end count into a jump list: one chip per
// referencing instance, click switches the workbench straight to it. No props
// -- both call sites (ProfileFormFields.vue's badge, ProfileManagerView.vue's
// delete hint) are about the same activeProfile, so this reads
// referencingInstances directly off profile-context.ts rather than having two
// call sites thread the same array down as a prop.
//
// actions.selectInstance() (bridge.ts, implemented by services/navigation.ts)
// already refuses to run while chrome.profileBusy -- see that function's own
// comment on why: it is the one guard every navigation caller shares, so a
// click here mid save/delete/refresh cannot strand that operation. Disabling
// the button on the same flag is belt-and-suspenders UI feedback, not the
// actual guard.
import { actions, chrome } from "../../bridge.ts";
import { referencingInstances } from "./profile-context.ts";
</script>

<template>
  <p v-if="referencingInstances.length" class="profile-reference-jump" aria-label="引用该配置档的实例，点击可切换">
    <button
      v-for="instance in referencingInstances"
      :key="instance.id"
      type="button"
      :disabled="chrome.profileBusy"
      :title="`切换到实例「${instance.name || instance.id}」`"
      @click="actions.selectInstance(instance.id)"
    >{{ instance.name || instance.id }}</button>
  </p>
</template>
