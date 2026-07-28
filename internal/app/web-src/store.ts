import { reactive } from "vue";
import { createState } from "./state.ts";

// The single state object, shared between the Vue chrome and the not-yet-migrated
// vanilla code in app.js. Wrapping it in reactive() is what makes incremental
// migration work: app.js keeps mutating it as a plain object (`state.instances = …`)
// and every Vue component reading it re-renders on its own, with no explicit
// render() call. Do not replace the object itself — mutate its fields.
export const store = reactive(createState());
