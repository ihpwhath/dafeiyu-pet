(() => {
  "use strict";

  const reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  const characters = {
    v1: {
      name: "大肥鱼",
      tag: "逐帧动画",
      desc: "106 个动作，从待机、吃饭到写代码和放烟花；加装 plus 后扩充到 268 个。",
      type: "video",
      src: "assets/chars/v1.webm",
    },
    gptv1: {
      name: "GPT娘v1",
      tag: "73 帧图集",
      desc: "白发龙娘加专属语录，待机、奔跑、挥手和 16 方向视线。",
      type: "image",
      src: "assets/chars/gptv1.webp",
    },
    gptv2: {
      name: "GPT娘v2",
      tag: "逐帧动画",
      desc: "92 个动作，按需下载；从吃饭、节日到工作状态都有自己的回应。",
      type: "image",
      src: "assets/chars/gptv2.webp",
    },
    claude: {
      name: "Claude",
      tag: "逐帧动画",
      desc: "166 个动作，规矩多、爱审查的 Claude 娘，台词自带东八区自嘲。",
      type: "image",
      src: "assets/chars/claude.webp",
    },
    gemini: {
      name: "Gemini",
      tag: "逐帧动画",
      desc: "100 个动作，哈基米猫娘；吃喝、玩耍、节日和工作都有自己的动作。",
      type: "image",
      src: "assets/chars/gemini.webp",
    },
    glm: {
      name: "GLM",
      tag: "逐帧动画",
      desc: "97 个动作，嗜睡蓝狐娘；才艺、礼仪、余额和碎碎念一应俱全。",
      type: "image",
      src: "assets/chars/glm.webp",
    },
    grok: {
      name: "Grok",
      tag: "逐帧动画",
      desc: "106 个动作，金发小魔女；嘴欠、实时联网，连余额都带着嘲讽。",
      type: "image",
      src: "assets/chars/grok.webp",
    },
    kimi: {
      name: "Kimi",
      tag: "逐帧动画",
      desc: "105 个动作，星芒小女仆；长文本与月之暗面的日常。",
      type: "image",
      src: "assets/chars/kimi.webp",
    },
    qwen: {
      name: "Qwen",
      tag: "逐帧动画",
      desc: "106 个动作，算力管够的大小姐；情绪、才艺、吃喝与工作状态都有覆盖。",
      type: "image",
      src: "assets/chars/qwen.webp",
    },
  };


  const video = document.getElementById("character-video");
  const image = document.getElementById("character-image");
  const tag = document.getElementById("character-tag");
  const name = document.getElementById("character-name");
  const desc = document.getElementById("character-desc");
  const options = [...document.querySelectorAll("[data-character]")];

  let characterInView = true;
  const preload = new Set();
  Object.values(characters).forEach((character) => {
    if (character.type !== "image" || preload.has(character.src)) return;
    preload.add(character.src);
    const img = new Image();
    img.src = character.src;
  });

  function selectCharacter(id) {
    const character = characters[id];
    if (!character) return;

    options.forEach((button) => {
      button.setAttribute("aria-selected", String(button.dataset.character === id));
    });

    tag.textContent = character.tag;
    name.textContent = character.name;
    desc.textContent = character.desc;

    if (character.type === "video") {
      image.classList.add("hide");
      video.classList.remove("hide");
      if (!reduceMotion && characterInView && !document.hidden) {
        const playback = video.play();
        if (playback && playback.catch) playback.catch(() => {});
      } else {
        video.pause();
      }
      return;
    }

    video.pause();
    video.classList.add("hide");
    image.src = character.src;
    image.alt = `${character.name} 立绘`;
    image.classList.remove("hide");
  }

  options.forEach((button) => {
    button.addEventListener("click", () => selectCharacter(button.dataset.character));
  });

  const characterSection = document.getElementById("characters");
  if (characterSection && "IntersectionObserver" in window && video) {
    const observer = new IntersectionObserver(
      ([entry]) => {
        characterInView = entry.isIntersecting;
        if (!characterInView || video.classList.contains("hide") || reduceMotion || document.hidden) {
          video.pause();
          return;
        }
        const playback = video.play();
        if (playback && playback.catch) playback.catch(() => {});
      },
      { threshold: 0.08 },
    );
    observer.observe(characterSection);
  }

  document.addEventListener("visibilitychange", () => {
    if (!video || video.classList.contains("hide") || reduceMotion) return;
    if (document.hidden || !characterInView) {
      video.pause();
      return;
    }
    const playback = video.play();
    if (playback && playback.catch) playback.catch(() => {});
  });

  if (reduceMotion && video) video.pause();

  const navToggle = document.querySelector(".menu-toggle");
  const navLinks = document.getElementById("nav-links");

  function closeNav() {
    document.body.classList.remove("nav-open");
    if (navToggle) navToggle.setAttribute("aria-expanded", "false");
  }

  if (navToggle && navLinks) {
    navToggle.addEventListener("click", () => {
      const open = !document.body.classList.contains("nav-open");
      document.body.classList.toggle("nav-open", open);
      navToggle.setAttribute("aria-expanded", String(open));
    });

    navLinks.addEventListener("click", (event) => {
      if (event.target.closest("a")) closeNav();
    });

    document.addEventListener("click", (event) => {
      if (!document.body.classList.contains("nav-open")) return;
      if (event.target.closest(".nav-shell")) return;
      closeNav();
    });

    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape") closeNav();
    });
  }

  const header = document.querySelector(".site-header");
  let scrollTick = false;

  function updateHeader() {
    if (header) header.classList.toggle("is-scrolled", window.scrollY > 8);
    scrollTick = false;
  }

  window.addEventListener(
    "scroll",
    () => {
      if (scrollTick) return;
      scrollTick = true;
      window.requestAnimationFrame(updateHeader);
    },
    { passive: true },
  );

  updateHeader();

  const revealElements = [...document.querySelectorAll("[data-reveal]")];
  if (revealElements.length) {
    document.documentElement.classList.add("js");
    if ("IntersectionObserver" in window && !reduceMotion) {
      const revealObserver = new IntersectionObserver(
        (entries) => {
          entries.forEach((entry) => {
            if (!entry.isIntersecting) return;
            entry.target.classList.add("is-visible");
            revealObserver.unobserve(entry.target);
          });
        },
        { rootMargin: "0px 0px -8% 0px", threshold: 0.08 },
      );
      revealElements.forEach((element) => revealObserver.observe(element));
    } else {
      revealElements.forEach((element) => element.classList.add("is-visible"));
    }
  }
})();
