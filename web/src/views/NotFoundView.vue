<script setup lang="ts">
import EmptyState from '@/components/ui/EmptyState.vue'
import { useRouter } from 'vue-router'

const router = useRouter()
</script>

<template>
  <EmptyState framed title="Nothing here" text="This page doesn't exist." action="Back to overview" @action="router.push({ name: 'overview' })" />
</template>
