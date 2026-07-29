<template>
  <!-- 舰队徽标：三个编队前行的三角帆（一大二小的 V 字编队），替换旧的 "MF"
       文字。填充用 currentColor，颜色与底色来自宿主 .mark / .empty-mark，
       尺寸由宿主的 svg 规则控制（shell.css）。favicon.svg（web-src/public/）
       是同一图形加上烘焙进去的渐变底，两处改动要同步。 -->
  <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
    <path d="M11.5 6.9L20 12L11.5 17.1L13.9 12Z" />
    <path d="M4.2 2.4L9.8 5.8L4.2 9.2L5.8 5.8Z" />
    <path d="M4.2 14.8L9.8 18.2L4.2 21.6L5.8 18.2Z" />
  </svg>
</template>
