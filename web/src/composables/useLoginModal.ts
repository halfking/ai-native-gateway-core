import { ref } from 'vue'

const showLoginModal = ref(false)

export function useLoginModal() {
  function openLogin() {
    showLoginModal.value = true
  }

  function closeLogin() {
    showLoginModal.value = false
  }

  return { showLoginModal, openLogin, closeLogin }
}
