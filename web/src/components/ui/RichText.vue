<script setup lang="ts">
// A description with its structure (headings, lists, code), rendered as
// text: see lib/richtext. A long one is folded behind "Show all" so what
// follows it stays in reach.
import { computed, h, ref, watch, type FunctionalComponent } from 'vue'
import { parseRichText, type Inline } from '@/lib/richtext'

const props = withDefaults(defineProps<{ text: string; foldAt?: number }>(), { foldAt: 700 })
const blocks = computed(() => parseRichText(props.text))
const long = computed(() => props.text.length > props.foldAt)
const open = ref(false)
watch(() => props.text, () => (open.value = false))

// Spans renders inline text: `code` and **bold**, the rest as text.
const Spans: FunctionalComponent<{ s: Inline[] }> = (p) =>
  p.s.map((x) => (x.kind === 'code' ? h('code', x.text) : x.kind === 'strong' ? h('strong', x.text) : x.text))
</script>

<template>
  <div>
    <div class="rich" :class="{ folded: long && !open }">
      <template v-for="(b, i) in blocks" :key="i">
        <h4 v-if="b.kind === 'heading'" class="h"><Spans :s="b.text" /></h4>
        <p v-else-if="b.kind === 'paragraph'" class="p"><Spans :s="b.text" /></p>
        <ul v-else-if="b.kind === 'list'" class="ul">
          <li v-for="(item, k) in b.items" :key="k" :style="{ marginLeft: `${item.depth * 16}px` }"><Spans :s="item.text" /></li>
        </ul>
        <pre v-else class="pre"><code>{{ b.text }}</code></pre>
      </template>
    </div>
    <button v-if="long" type="button" class="more" :aria-expanded="open" @click="open = !open">{{ open ? 'Show less' : 'Show all' }}</button>
  </div>
</template>

<style scoped>
.rich { font-size: 14px; line-height: 1.5; overflow-wrap: anywhere; }
.rich > * + * { margin-top: 10px; }
.folded { max-height: 260px; overflow: hidden; mask-image: linear-gradient(to bottom, black 70%, transparent); }
.h { font-size: 12px; font-weight: 600; letter-spacing: 0.04em; text-transform: uppercase; color: var(--muted); margin-top: 18px; }
.h:first-child { margin-top: 0; }
.p { white-space: pre-wrap; }
.ul { margin: 0; padding-left: 18px; }
.ul li + li { margin-top: 4px; }
/* :deep: Spans makes these, outside this component's scope. */
.rich :deep(code) { font-family: var(--font-mono); font-size: 0.88em; padding: 1px 4px; background: var(--rule); border-radius: 3px; }
.pre { margin: 0; padding: 12px; background: var(--paper); border: 1px solid var(--rule); border-radius: var(--radius); overflow: auto; }
.rich .pre :deep(code) { padding: 0; background: none; white-space: pre; }
.more { margin-top: 8px; padding: 0; border: 0; background: none; color: var(--cobalt); font: inherit; font-size: 13px; font-weight: 500; cursor: pointer; }
</style>
