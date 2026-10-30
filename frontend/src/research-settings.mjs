export const profileMinutes = Object.freeze({ quick: 5, standard: 30, deep_laptop: 120, long_laptop: 240, overnight_laptop: 360 })
export const maxOutputTokens = 5120
export const defaultSetup = Object.freeze({ profile: 'standard', timeBudgetMinutes: 30, maxOutputTokens, temperature: 0.2, topP: 0.8 })

const within = (value, min, max) => typeof value === 'number' && Number.isFinite(value) && value >= min && value <= max

export function conversationSetup(conversation) {
  const research = conversation?.research_settings
  const model = conversation?.model_profile
  const profile = Object.hasOwn(profileMinutes, research?.profile) ? research.profile : defaultSetup.profile
  const minutes = research?.time_budget_seconds / 60
  return {
    profile,
    timeBudgetMinutes: within(minutes, 1, profileMinutes[profile]) ? minutes : profileMinutes[profile],
    maxOutputTokens: within(model?.max_output_tokens, 1, maxOutputTokens) && Number.isInteger(model.max_output_tokens) ? model.max_output_tokens : defaultSetup.maxOutputTokens,
    temperature: within(model?.temperature, 0, 2) ? model.temperature : defaultSetup.temperature,
    topP: within(model?.top_p, Number.MIN_VALUE, 1) ? model.top_p : defaultSetup.topP,
  }
}

export function setupProblem(setup) {
  if (!Object.hasOwn(profileMinutes, setup.profile)) return 'Выберите профиль исследования.'
  if (!within(setup.timeBudgetMinutes, 1, profileMinutes[setup.profile])) return `Укажите лимит времени от 1 до ${profileMinutes[setup.profile]} мин для выбранного профиля.`
  if (!within(setup.maxOutputTokens, 1, maxOutputTokens) || !Number.isInteger(setup.maxOutputTokens)) return `Укажите целое число выходных токенов от 1 до ${maxOutputTokens}.`
  if (!within(setup.temperature, 0, 2)) return 'Укажите температуру от 0 до 2.'
  if (!within(setup.topP, Number.MIN_VALUE, 1)) return 'Укажите top-p больше 0 и не больше 1.'
  return ''
}

export function settingsPatch(setup) {
  const problem = setupProblem(setup)
  if (problem) throw new RangeError(problem)
  return {
    research_settings: { profile: setup.profile, time_budget_seconds: Math.round(setup.timeBudgetMinutes * 60) },
    model_profile: { max_output_tokens: setup.maxOutputTokens, temperature: setup.temperature, top_p: setup.topP },
  }
}
